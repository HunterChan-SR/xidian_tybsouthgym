package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func oracleStdioTestCommand(t *testing.T, mode string) []string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{path, "-test.run=^TestOracleStdioProcess$", "--", mode}
}

// A real child process tests pipe ownership, exit, timeout and response framing.
func TestOracleStdioProcess(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-3] != "--" || args[len(args)-1] != "--stdio" {
		return
	}
	mode := args[len(args)-2]
	if mode == "startup-exit" {
		os.Exit(2)
	}
	encoder, decoder := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	encoder.Encode(map[string]bool{"ready": mode != "not-ready"})
	for {
		var request struct {
			Deadline  time.Time `json:"deadline"`
			Challenge struct {
				Token string `json:"token"`
			} `json:"challenge"`
		}
		if decoder.Decode(&request) != nil {
			os.Exit(0)
		}
		if !time.Now().Before(request.Deadline) {
			os.Exit(2)
		}
		reply := map[string]any{"token": request.Challenge.Token, "x": 123}
		switch mode {
		case "exit":
			os.Exit(2)
		case "timeout":
			time.Sleep(10 * time.Second)
		case "wrong-token":
			reply["token"] = "old-token"
		case "bounds":
			reply["x"] = 251
		case "cancel":
			reply = map[string]any{"cancelled": true}
		}
		encoder.Encode(reply)
	}
}

func TestPersistentOracleFailureNeverOrders(t *testing.T) {
	background, piece := oracleTestImages(t)
	for _, mode := range []string{"wrong-token", "bounds", "cancel", "exit", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config(t)
			cfg.BookingCaptcha, cfg.CaptchaSolverPersistent = true, true
			cfg.CaptchaSolverCommand = oracleStdioTestCommand(t, mode)
			orders := 0
			c := fake(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Field/GetFieldSliderCaptcha" {
					json.NewEncoder(w).Encode(map[string]any{"type": 1, "token": "current-token", "bg": background, "piece": piece})
				} else {
					orders++
					t.Error("worker failure reached an order endpoint")
				}
			})
			cleanup, err := attachOracleWorker(context.Background(), []*Client{c}, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			worker := c.oracle
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			candidates, _ := choose(pair("YMQ004", "12"), cfg, 10)
			_, err = guardedSubmit(ctx, c, cfg, candidates[0], "2026-11-10")
			if err == nil || orders != 0 {
				t.Fatal(err, orders)
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if _, err := os.Stat(receiptPath(cfg, c.Session, "2026-11-10")); !os.IsNotExist(err) {
				t.Fatal("unexpected guard", err)
			}
			files, _ := filepath.Glob(filepath.Join(cfg.StateDir, "captcha", "*"))
			if len(files) != 0 {
				t.Fatal("stdio unexpectedly used challenge files", files)
			}
			cleanup()
			select {
			case <-worker.done:
			default:
				t.Fatal("worker was not reaped")
			}
		})
	}
}

func TestPersistentOracleStartupFailureAndSharedLifetime(t *testing.T) {
	cfg := config(t)
	cfg.BookingCaptcha, cfg.CaptchaSolverPersistent = true, true
	for _, mode := range []string{"startup-exit", "not-ready"} {
		cfg.CaptchaSolverCommand = oracleStdioTestCommand(t, mode)
		if w, err := startOracleWorker(context.Background(), cfg); err == nil {
			w.close()
			t.Fatal("invalid startup accepted")
		}
	}
	cfg.CaptchaSolverCommand = oracleStdioTestCommand(t, "success")
	clients := []*Client{{Name: "one"}, {Name: "two"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup, err := attachOracleWorker(ctx, clients, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	w := clients[0].oracle
	if w != clients[1].oracle {
		t.Fatal("account pool should share a serial worker")
	}
	for i, c := range clients {
		token := fmt.Sprint("token-", i)
		values, err := c.solveCaptcha(ctx, cfg, []byte(fmt.Sprintf(`{"token":%q}`, token)), token, 250)
		if err != nil || values.Get("SliderToken") != token {
			t.Fatal(values, err)
		}
	}
	cancel()
	select {
	case <-w.done:
	case <-time.After(3 * time.Second):
		t.Fatal("parent cancellation left a process alive")
	}
	cleanup()
	if clients[0].oracle != nil || clients[1].oracle != nil {
		t.Fatal("worker was not detached")
	}
}
