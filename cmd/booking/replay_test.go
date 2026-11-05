package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Sanitized observed response from 2026-10-19. All other data in this file is
// synthetic, served only by httptest; no real account or booking endpoint is used.
const historicalInventoryBusy = "{\"type\":\"0\",\"message\":\"访问人数过多，请稍后刷新重试\"}"

func TestReplayReleaseLifecycle(t *testing.T) {
	cfg := config(t)
	cfg.PoolID = "replay-release"
	cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
	queries, orders, successes := 0, 0, 0
	var busyAt time.Time
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			json.NewEncoder(w).Encode(map[string]int{"limitcount": 10})
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			queries++
			if queries == 1 {
				rows := pair("YMQ004", "12")
				for i := range rows {
					rows[i].TimeStatus = json.RawMessage("0")
				}
				json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": rows})
			} else if queries == 2 {
				busyAt = time.Now()
				fmt.Fprint(w, historicalInventoryBusy)
			} else {
				if time.Since(busyAt) < 5*time.Second {
					t.Error("busy response was not backed off")
				}
				field := "YMQ004"
				if orders > 0 {
					field = "YMQ001"
				}
				json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": pair(field, "12")})
			}
		case "/Field/OrderField":
			orders++
			var items []Item
			if json.Unmarshal([]byte(r.URL.Query().Get("checkdata")), &items) != nil || len(items) != 2 || r.URL.Query().Get("dateadd") != "2" {
				t.Error("invalid submitted target")
			}
			if orders == 1 {
				json.NewEncoder(w).Encode(map[string]any{"type": 2, "message": "场地已被预订"})
			} else {
				successes++
				json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": "replay-order"})
			}
		case "/Field/GetPayInfo":
			date := targetDate(cfg, time.Now())
			json.NewEncoder(w).Encode([]map[string]string{{"FieldNo": "YMQ001", "OrderDate": date, "BeginTime": "19:00", "EndTime": "20:00", "Price": "12"}, {"FieldNo": "YMQ001", "OrderDate": date, "BeginTime": "20:00", "EndTime": "21:00", "Price": "12"}})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	}
	first, second := fake(t, handler), fake(t, handler)
	first.Name = "one"
	first.Session.Cookie = "UserId=replay-one"
	second.Name = "two"
	second.Session.Cookie = "UserId=replay-two"
	first.EventLog = filepath.Join(t.TempDir(), "events.jsonl")
	second.EventLog = first.EventLog
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := runPool(ctx, []*Client{first, second}, cfg, "book")
	if err != nil || out.Status != "reserved_payment_manual" || out.TotalCents != 2400 || out.FieldNo != "YMQ001" || queries != 4 || orders != 2 || successes != 1 {
		t.Fatal(out, err, queries, orders, successes)
	}
	b, err := os.ReadFile(first.EventLog)
	if err != nil || !strings.Contains(string(b), "unavailableSlotCount") || !strings.Contains(string(b), "inventory_busy") {
		t.Fatal(err, string(b))
	}
	// Restart protection is shared by the whole identity pool, even after success.
	_, err = runPool(ctx, []*Client{second}, cfg, "book")
	if faultKind(err) != "existing_order_or_uncertain_attempt" || orders != 2 {
		t.Fatal(err, orders)
	}
}

func TestReplayObservationOnlyLatchAcrossPool(t *testing.T) {
	for _, kind := range []string{"schema_changed", "date_mapping_changed", "price_missing_or_invalid", "duplicate_slot", "unexpected_response"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			cfg := config(t)
			cfg.PoolID = "read-only-replay"
			cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
			queries, calendars, orders := 0, 0, 0
			handler := func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/User/CheckUserStatus":
					fmt.Fprint(w, "1")
				case "/Field/GetOrderInfo":
					json.NewEncoder(w).Encode(map[string]int{"limitcount": 10})
				case "/Field/GetWeek":
					calendars++
					if kind == "date_mapping_changed" && calendars == 1 {
						json.NewEncoder(w).Encode([]map[string]string{{"Date": "01-01"}, {"Date": "01-01"}, {"Date": "01-01"}})
					} else {
						fmt.Fprint(w, dateJSON(cfg))
					}
				case "/Field/GetVenueStateNew":
					queries++
					rows := pair("YMQ004", "12")
					if queries == 1 {
						switch kind {
						case "schema_changed":
							fmt.Fprint(w, "{}")
							return
						case "unexpected_response":
							fmt.Fprint(w, "<html>temporary unknown page</html>")
							return
						case "price_missing_or_invalid":
							rows[0].FinalPrice = nil
						case "duplicate_slot":
							rows = append(rows, rows[0])
						}
					}
					json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": rows})
				default:
					orders++
					t.Error("read-only latch allowed a mutation", r.URL.Path)
				}
			}
			first, second := fake(t, handler), fake(t, handler)
			first.Name = "one"
			first.Session.Cookie = "UserId=latch-one"
			second.Name = "two"
			second.Session.Cookie = "UserId=latch-two"
			first.EventLog = filepath.Join(t.TempDir(), "events.jsonl")
			second.EventLog = first.EventLog
			ctx, cancel := context.WithTimeout(context.Background(), 6500*time.Millisecond)
			defer cancel()
			out, err := runPool(ctx, []*Client{first, second}, cfg, "book")
			if faultKind(err) != "observation_only_window_closed" || out.ObservationOnlyReason != kind || orders != 0 || calendars != 2 {
				t.Fatal(out, err, orders, calendars)
			}
			b, err := os.ReadFile(first.EventLog)
			if err != nil || strings.Count(string(b), "\"event\":\"observation_only\"") != 1 || !strings.Contains(string(b), "\"observationOnly\":true") {
				t.Fatal(err, string(b))
			}
		})
	}
}

func TestReplayUnknownQueryDoesNotExtendDeadline(t *testing.T) {
	cfg := config(t)
	cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
	queries := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			json.NewEncoder(w).Encode(map[string]int{"limitcount": 10})
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			queries++
			fmt.Fprint(w, "{}")
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	out, err := run(ctx, c, cfg, "book")
	if faultKind(err) != "observation_only_window_closed" || out.ObservationOnlyReason != "schema_changed" || queries != 1 {
		t.Fatal(out, err, queries)
	}
}

func TestReplayRateLimitWaitsAcrossEntirePool(t *testing.T) {
	cfg := config(t)
	cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
	queries := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			json.NewEncoder(w).Encode(map[string]int{"limitcount": 10})
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			queries++
			w.Header().Set("Retry-After", "9")
			w.WriteHeader(429)
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	}
	first, second := fake(t, handler), fake(t, handler)
	first.Session.Cookie = "UserId=limit-one"
	second.Session.Cookie = "UserId=limit-two"
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	_, err := runPool(ctx, []*Client{first, second}, cfg, "book")
	if faultKind(err) != "window_closed" || queries != 1 {
		t.Fatal(err, queries)
	}
}
