package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestProbeNeverOrders(t *testing.T) {
	cfg := config(t)
	orders := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			fmt.Fprint(w, "{\"limitcount\":10}")
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": pair("YMQ004", "12")})
		default:
			orders++
			t.Error("unexpected endpoint", r.URL.Path)
		}
	})
	out, e := run(context.Background(), c, cfg, "probe")
	if e != nil || out.Status != "available" || orders != 0 {
		t.Fatal(out, e, orders)
	}
}

func TestPoolStopsAcrossAccounts(t *testing.T) {
	cfg := config(t)
	cfg.PoolID = "shared-goal"
	calls := 0
	c1 := fake(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, "{\"type\":1,\"resultdata\":\"first-order\"}")
	})
	c2 := fake(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, "{\"type\":1,\"resultdata\":\"second-order\"}")
	})
	c1.Session.Cookie = "UserId=account-one"
	c2.Session.Cookie = "UserId=account-two"
	cs, _ := choose(pair("YMQ004", "12"), cfg, 10)
	_, e := guardedSubmit(context.Background(), c1, cfg, cs[0], "2026-10-20")
	if e != nil {
		t.Fatal(e)
	}
	_, e = guardedSubmit(context.Background(), c2, cfg, cs[0], "2026-10-20")
	if faultKind(e) != "existing_order_or_uncertain_attempt" || calls != 1 {
		t.Fatal(e, calls)
	}
}

func TestServerClockBounds(t *testing.T) {
	c := &Client{ClockOffsets: []time.Duration{2 * time.Second, 1800 * time.Millisecond, 2100 * time.Millisecond}}
	offset, e := c.clockOffset()
	if e != nil || offset != 2*time.Second {
		t.Fatal(offset, e)
	}
	c.ClockOffsets = []time.Duration{time.Minute}
	if _, e = c.clockOffset(); faultKind(e) != "clock_skew_too_large" {
		t.Fatal(e)
	}
}

func TestPoolCompetitionFallbackStopsAfterOneSuccess(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint("persistent=", persistent), func(t *testing.T) {
			testPoolCompetitionFallbackStopsAfterOneSuccess(t, persistent)
		})
	}
}

func testPoolCompetitionFallbackStopsAfterOneSuccess(t *testing.T, persistent bool) {
	cfg := config(t)
	background, piece := oracleTestImages(t)
	cfg.BookingCaptcha = true
	cfg.CaptchaSolverCommand = randomOracleTestCommand(t)
	cfg.CaptchaSolverPersistent = persistent
	cfg.PoolID = "integration"
	cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
	orders := []string{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			json.NewEncoder(w).Encode(map[string]int{"limitcount": 10})
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetFieldSliderCaptcha":
			cookie, err := r.Cookie("UserId")
			if err != nil {
				t.Error(err)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "token": "challenge-" + cookie.Value, "bg": background, "piece": piece})
		case "/Field/GetVenueStateNew":
			slots := pair("YMQ001", "12")
			if len(orders) == 0 {
				slots = append(slots, pair("YMQ004", "12")...)
			}
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": slots})
		case "/Field/OrderField":
			cookie, err := r.Cookie("UserId")
			x, coordinateErr := strconv.Atoi(r.URL.Query().Get("SliderX"))
			if err != nil || coordinateErr != nil || x < 0 || x > 250 || r.URL.Query().Get("SliderToken") != "challenge-"+cookie.Value {
				t.Error("order did not use its account's oracle answer")
				return
			}
			var items []Item
			json.Unmarshal([]byte(r.URL.Query().Get("checkdata")), &items)
			if len(items) != 2 {
				t.Error("wrong order items")
				return
			}
			orders = append(orders, items[0].FieldNo)
			if len(orders) == 1 {
				json.NewEncoder(w).Encode(map[string]any{"type": 3, "message": "抱歉(T_T) 您手速不够快，场地【羽毛球4】已被其他人抢跑"})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": "single-order"})
			}
		case "/Field/GetPayInfo":
			date := targetDate(cfg, time.Now())
			json.NewEncoder(w).Encode([]map[string]string{{"FieldNo": "YMQ001", "OrderDate": date, "BeginTime": "19:00", "EndTime": "20:00", "Price": "12"}, {"FieldNo": "YMQ001", "OrderDate": date, "BeginTime": "20:00", "EndTime": "21:00", "Price": "12"}})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}
	first := fake(t, handler)
	first.Name = "one"
	first.Session.Cookie = "UserId=one"
	second := fake(t, handler)
	second.Name = "two"
	second.Session.Cookie = "UserId=two"
	for _, client := range []*Client{first, second} {
		baseURL, _ := url.Parse(client.Base)
		client.HTTP.Jar.SetCookies(baseURL, []*http.Cookie{{Name: "UserId", Value: client.Name}})
	}
	out, err := runPool(context.Background(), []*Client{first, second}, cfg, "book")
	if first.oracle != nil || second.oracle != nil {
		t.Fatal("pool left a worker attached")
	}
	if err != nil || out.Status != "reserved_payment_manual" || out.Account != "two" || out.FieldNo != "YMQ001" || !reflect.DeepEqual(orders, []string{"YMQ004", "YMQ001"}) {
		t.Fatal(out, err, orders)
	}
	_, err = runPool(context.Background(), []*Client{first, second}, cfg, "book")
	if faultKind(err) != "existing_order_or_uncertain_attempt" || len(orders) != 2 {
		t.Fatal(err, orders)
	}
}

func TestPoolLostOrderResponseStopsOtherAccounts(t *testing.T) {
	cfg := config(t)
	cfg.OrderTLSPreconnect = true
	cfg.PoolID = "lost-response"
	cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
	orders := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			json.NewEncoder(w).Encode(map[string]int{"limitcount": 10})
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": pair("YMQ004", "12")})
		case "/Field/OrderField":
			orders++
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}
	first := fake(t, handler)
	first.Session.Cookie = "UserId=one"
	second := fake(t, handler)
	second.Session.Cookie = "UserId=two"
	out, err := runPool(context.Background(), []*Client{first, second}, cfg, "book")
	if faultKind(err) != "order_uncertain" || orders != 1 || out.Status != "uncertain" {
		t.Fatal(out, err, orders)
	}
	_, err = runPool(context.Background(), []*Client{second}, cfg, "book")
	if faultKind(err) != "existing_order_or_uncertain_attempt" || orders != 1 {
		t.Fatal(err, orders)
	}
}

// A known read-only overload response must back off, recover, and never submit in watch mode.
func TestWatchRecoversFromInventoryBusy(t *testing.T) {
	cfg := config(t)
	cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
	queries := 0
	var first time.Time
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			fmt.Fprint(w, "{\"limitcount\":10}")
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			queries++
			if queries == 1 {
				first = time.Now()
				json.NewEncoder(w).Encode(map[string]any{"type": 0, "message": "访问人数过多，请稍后刷新重试"})
				return
			}
			if time.Since(first) < 5*time.Second {
				t.Error("inventory busy retried without conservative backoff")
			}
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": pair("YMQ004", "12")})
		default:
			t.Error("watch mode called unexpected endpoint", r.URL.Path)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := run(ctx, c, cfg, "watch")
	if err != nil || out.Status != "available" || queries != 2 {
		t.Fatal(out, err, queries)
	}
}

func TestInventoryBusyStopsAtDeadline(t *testing.T) {
	cfg := config(t)
	cfg.ReleaseTime = time.Now().In(shanghai).Add(-time.Second).Format("15:04:05")
	queries := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			fmt.Fprint(w, "{\"limitcount\":10}")
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			queries++
			json.NewEncoder(w).Encode(map[string]any{"type": 0, "message": "访问人数过多，请稍后刷新重试"})
		default:
			t.Error("unexpected request after inventory failure", r.URL.Path)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := run(ctx, c, cfg, "book")
	if faultKind(err) != "window_closed" || queries != 1 {
		t.Fatal(err, queries)
	}
}
