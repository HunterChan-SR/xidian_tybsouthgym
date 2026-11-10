// 可选的本轮 oracle 子进程：放票前启动，串行交接，本轮结束即回收。
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type oracleWorker struct {
	exchangeMu sync.Mutex
	input      io.WriteCloser
	output     io.ReadCloser
	encode     *json.Encoder
	decode     *json.Decoder
	cancel     context.CancelFunc
	done       chan struct{}
	closeOnce  sync.Once
}

func startOracleWorker(ctx context.Context, cfg Config) (*oracleWorker, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	command := append(append([]string(nil), cfg.CaptchaSolverCommand...), "--stdio")
	cmd := exec.CommandContext(life, command[0], command[1:]...)
	cmd.Dir = filepath.Dir(executable)
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	// Own the read pipe: Cmd.Wait must not close it while the last reply is read.
	output, writer, err := os.Pipe()
	if err != nil {
		input.Close()
		cancel()
		return nil, err
	}
	cmd.Stdout = writer
	// stderr is intentionally discarded, just like the external file oracle.
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		writer.Close()
		cancel()
		return nil, &Fault{Kind: "booking_captcha_not_completed"}
	}
	writer.Close()
	w := &oracleWorker{input: input, output: output, encode: json.NewEncoder(input),
		decode: json.NewDecoder(output), cancel: cancel, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(w.done) }()
	startup, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err = w.exchange(startup, nil, &ready); err != nil || !ready.Ready {
		w.close()
		return nil, &Fault{Kind: "booking_captcha_not_completed"}
	}
	return w, nil
}

// Serial JSON handoff even when two account requests overlap on the network.
// Cancellation kills and reaps this worker; no automatic restart or replay.
func (w *oracleWorker) exchange(ctx context.Context, request, reply any) error {
	w.exchangeMu.Lock()
	defer w.exchangeMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		if request != nil {
			if err := w.encode.Encode(request); err != nil {
				result <- err
				return
			}
		}
		result <- w.decode.Decode(reply)
	}()
	select {
	case <-ctx.Done():
		w.close()
		return ctx.Err()
	case err := <-result:
		if err != nil {
			w.close()
			return &Fault{Kind: "booking_captcha_not_completed"}
		}
		return ctx.Err()
	}
}

func (w *oracleWorker) close() {
	w.closeOnce.Do(func() {
		w.input.Close()
		w.cancel()
		w.output.Close()
		<-w.done
	})
}

func attachOracleWorker(ctx context.Context, clients []*Client, cfg Config) (func(), error) {
	if !cfg.CaptchaSolverPersistent {
		return func() {}, nil
	}
	started := time.Now()
	w, err := startOracleWorker(ctx, cfg)
	if err != nil {
		return nil, err
	}
	for _, c := range clients {
		c.oracle = w
	}
	if len(clients) > 0 {
		clients[0].event(map[string]any{"event": "booking_oracle_started", "elapsedMs": time.Since(started).Milliseconds(), "transport": "stdio"})
	}
	return func() {
		w.close()
		for _, c := range clients {
			c.oracle = nil
		}
	}, nil
}
