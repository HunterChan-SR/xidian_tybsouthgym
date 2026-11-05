package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A subprocess exercises the real CLI entry point, exit code, final files and
// restart guard. Transport redirection exists only in this _test.go file.
func TestReplayCLIProcess(t *testing.T) {
	for _, scenario := range []string{"unknown-query", "lost-order-response"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			cfg := config(t)
			cfg.PoolID = "cli-replay"
			cfg.WindowSeconds = 8
			cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
			cfg.CookieFile = filepath.Join(cfg.StateDir, "session.json")
			configPath := filepath.Join(cfg.StateDir, "config.json")
			if err := saveJSON(cfg.CookieFile, Session{Cookie: "UserId=synthetic-cli-session"}); err != nil {
				t.Fatal(err)
			}
			if err := saveJSON(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			runChild := func() Outcome {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestReplayCLIHelper$")
				cmd.Env = append(os.Environ(), "BOOKING_TEST_SCENARIO="+scenario, "BOOKING_TEST_CONFIG="+configPath)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("unexpected CLI result: %v %s", err, output)
				}
				var out Outcome
				b, err := os.ReadFile(filepath.Join(cfg.StateDir, "latest.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(b, &out); err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(filepath.Join(cfg.StateDir, "runner.lock")); !os.IsNotExist(err) {
					t.Fatal("normal exit did not release lock", err)
				}
				return out
			}
			out := runChild()
			if scenario == "unknown-query" {
				if out.Status != "observation_only_window_closed" || out.ObservationOnlyReason != "schema_changed" {
					t.Fatal(out)
				}
			} else {
				if out.Status != "order_uncertain" {
					t.Fatal(out)
				}
				if _, err = os.Stat(receiptPath(cfg, Session{}, targetDate(cfg, time.Now()))); err != nil {
					t.Fatal("missing order protection", err)
				}
				out = runChild()
				if out.Status != "existing_order_or_uncertain_attempt" {
					t.Fatal("restart did not honor protection", out)
				}
			}
			b, err := os.ReadFile(filepath.Join(cfg.StateDir, "events", time.Now().In(shanghai).Format("2006-01-02")+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var submissions, ends, observations int
			for _, line := range strings.Split(string(b), "\n") {
				var event map[string]any
				if json.Unmarshal([]byte(line), &event) != nil {
					continue
				}
				switch event["event"] {
				case "submit":
					submissions++
				case "run_end":
					ends++
				case "inventory_observation":
					observations++
				}
			}
			if scenario == "unknown-query" && (submissions != 0 || ends != 1 || observations < 1) {
				t.Fatal(submissions, ends, observations)
			}
			if scenario == "lost-order-response" && (submissions != 1 || ends != 2) {
				t.Fatal(submissions, ends)
			}
		})
	}
}

func TestReplayCLIHelper(t *testing.T) {
	scenario := os.Getenv("BOOKING_TEST_SCENARIO")
	if scenario == "" {
		return
	}
	configPath := os.Getenv("BOOKING_TEST_CONFIG")
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			json.NewEncoder(w).Encode(map[string]int{"limitcount": 10})
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			queries++
			if scenario == "unknown-query" && queries == 1 {
				fmt.Fprint(w, "{}")
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": pair("YMQ004", "12")})
		case "/Field/OrderField":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		default:
			t.Error("unexpected CLI endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	// All requests, including the mutation transport clone, stay on loopback.
	http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "tybsouthgym.xidian.edu.cn:443" {
			return nil, fmt.Errorf("blocked test destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	flag.CommandLine = flag.NewFlagSet("booking-replay", flag.ExitOnError)
	os.Args = []string{"booking-replay", "-config", configPath, "-mode", "book"}
	main()
}
