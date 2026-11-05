package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestSubmitAndRestartGuard(t *testing.T) {
	cfg := config(t)
	calls := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/Field/OrderField" {
			t.Error(r.URL.Path)
		}
		var items []Item
		if json.Unmarshal([]byte(r.URL.Query().Get("checkdata")), &items) != nil || len(items) != 2 || items[1].Endtime != "21:00" {
			t.Error("bad payload")
		}
		fmt.Fprint(w, "{ \"type\" : 1, \"resultdata\" : \"OID-test\" }")
	})
	candidates, _ := choose(pair("YMQ004", "12"), cfg, 10)
	out, e := guardedSubmit(context.Background(), c, cfg, candidates[0], "2026-10-20")
	if e != nil || out.OrderID != "OID-test" || calls != 1 {
		t.Fatal(out, e, calls)
	}
	c.Session.Cookie = "UserId=test; session=refreshed"
	_, e = guardedSubmit(context.Background(), c, cfg, candidates[0], "2026-10-20")
	if faultKind(e) != "existing_order_or_uncertain_attempt" || calls != 1 {
		t.Fatal(e, calls)
	}
}

func TestUncertainOrderNotRetried(t *testing.T) {
	for _, body := range []string{"<html>error</html>", "{}", "not json"} {
		t.Run(body, func(t *testing.T) {
			cfg := config(t)
			calls := 0
			c := fake(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) })
			cand, _ := choose(pair("YMQ004", "12"), cfg, 10)
			_, e := guardedSubmit(context.Background(), c, cfg, cand[0], "2026-10-20")
			if faultKind(e) != "order_uncertain" {
				t.Fatal(e)
			}
			_, e = guardedSubmit(context.Background(), c, cfg, cand[0], "2026-10-20")
			if faultKind(e) != "existing_order_or_uncertain_attempt" || calls != 1 {
				t.Fatal(e, calls)
			}
		})
	}
}

func TestExplicitRejectionAllowsRefresh(t *testing.T) {
	cfg := config(t)
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "{\"type\":2,\"message\":\"下单速度过快\"}")
	})
	cand, _ := choose(pair("YMQ004", "12"), cfg, 10)
	_, e := guardedSubmit(context.Background(), c, cfg, cand[0], "2026-10-20")
	if faultKind(e) != "order_rate_limited" {
		t.Fatal(e)
	}
	if _, e = os.Stat(receiptPath(cfg, c.Session, "2026-10-20")); !os.IsNotExist(e) {
		t.Fatal("guard not released", e)
	}
}

func TestUnknownReplyKeepsGuard(t *testing.T) {
	cfg := config(t)
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "{\"type\":\"new_protocol\",\"message\":\"未知\"}")
	})
	cs, _ := choose(pair("YMQ004", "12"), cfg, 10)
	_, e := guardedSubmit(context.Background(), c, cfg, cs[0], "2026-10-20")
	if faultKind(e) != "order_uncertain" {
		t.Fatal(e)
	}
	if _, e = os.Stat(receiptPath(cfg, c.Session, "2026-10-20")); e != nil {
		t.Fatal(e)
	}
}

func TestTimeoutKeepsGuard(t *testing.T) {
	cfg := config(t)
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "{\"type\":1}")
	})
	c.HTTP.Timeout = 5 * time.Millisecond
	cs, _ := choose(pair("YMQ004", "12"), cfg, 10)
	_, e := guardedSubmit(context.Background(), c, cfg, cs[0], "2026-10-20")
	if faultKind(e) != "order_uncertain" {
		t.Fatal(e)
	}
	if _, e = os.Stat(receiptPath(cfg, c.Session, "2026-10-20")); e != nil {
		t.Fatal(e)
	}
}

func TestTestOrderLifecycle(t *testing.T) {
	cfg := config(t)
	cfg.TestOnly = true
	cfg.FieldTypeNo = "002"
	cfg.FieldPrefix = "PPQ"
	cfg.BeginTime = "18:00"
	cfg.EndTime = "19:00"
	cfg.DateAdd = 1
	cfg.MaxTotalCents = 600
	cfg.PreferredFields = []string{"PPQ002"}
	cfg.ExactDate = targetDate(cfg, time.Now())
	count, cancels := 0, 0
	status := "0"
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			fmt.Fprint(w, "{\"limitcount\":10}")
		case "/Field/GetWeek":
			fmt.Fprint(w, dateJSON(cfg))
		case "/Field/GetVenueStateNew":
			s := slot("PPQ002", "18:00", "19:00", "6")
			s.FieldTypeNo = "002"
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": []Slot{s}})
		case "/Field/OrderField":
			count++
			fmt.Fprint(w, "{\"type\":1,\"resultdata\":\"test-order\"}")
		case "/Field/GetPayInfo":
			json.NewEncoder(w).Encode([]map[string]string{{"FieldNo": "PPQ002", "OrderDate": cfg.ExactDate, "BeginTime": "18:00", "EndTime": "19:00", "Price": "6.00"}})
		case "/Field/CheckStatus":
			fmt.Fprintf(w, "[{\"PayStatus\":%q}]", status)
		case "/Field/FieldCancel":
			cancels++
			status = "4"
			fmt.Fprint(w, "{\"type\":1}")
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	})
	out, e := run(context.Background(), c, cfg, "test-book")
	if e != nil || out.Status != "test_cancelled" || count != 1 || cancels != 1 {
		t.Fatal(out, e, count, cancels)
	}
	_, e = run(context.Background(), c, cfg, "test-book")
	if e == nil || count != 1 {
		t.Fatal("test repeated", e, count)
	}
}
