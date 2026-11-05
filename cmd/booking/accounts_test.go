package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestPoolUsesRefreshedDuplicateSession(t *testing.T) {
	cfg := config(t)
	first := fake(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "0") })
	first.Session.Cookie = "UserId=same; token=expired"
	second := fake(t, func(w http.ResponseWriter, r *http.Request) {
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
			t.Error(r.URL.Path)
		}
	})
	second.Session.Cookie = "UserId=same; token=valid"
	out, e := runPool(context.Background(), []*Client{first, second}, cfg, "probe")
	if e != nil || out.Status != "available" {
		t.Fatal(out, e)
	}
}

func TestPreflightRecoversWithoutRetryingExpiredAccount(t *testing.T) {
	expiredChecks, busyChecks := 0, 0
	expired := fake(t, func(w http.ResponseWriter, r *http.Request) { expiredChecks++; fmt.Fprint(w, "0") })
	expired.Session.Cookie = "UserId=expired"
	busy := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/User/CheckUserStatus" {
			busyChecks++
			if busyChecks == 1 {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(429)
				return
			}
			fmt.Fprint(w, "1")
			return
		}
		fmt.Fprint(w, `{"limitcount":10}`)
	})
	busy.Session.Cookie = "UserId=usable"
	var delays []time.Duration
	ready, err := preparePoolWithRetry(context.Background(), []*Client{expired, busy}, func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil })
	if err != nil || len(ready) != 1 || ready[0].client != busy || expiredChecks != 1 || busyChecks != 2 || !reflect.DeepEqual(delays, []time.Duration{7 * time.Second}) {
		t.Fatal(err, len(ready), expiredChecks, busyChecks, delays)
	}
}

func TestPreflightRetriesAreBounded(t *testing.T) {
	checks := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) { checks++; w.WriteHeader(503) })
	var delays []time.Duration
	_, err := preparePoolWithRetry(context.Background(), []*Client{c}, func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil })
	if faultKind(err) != "server_error" || checks != 3 || !reflect.DeepEqual(delays, []time.Duration{2 * time.Second, 4 * time.Second}) {
		t.Fatal(err, checks, delays)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = preparePoolWithRetry(ctx, []*Client{c}, sleepContext)
	if err != context.Canceled || checks != 3 {
		t.Fatal(err, checks)
	}
}

func TestPrewarmReselectsFreshSession(t *testing.T) {
	freshChecks := 0
	expired := false
	primary := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/User/CheckUserStatus" {
			if expired {
				fmt.Fprint(w, "0")
			} else {
				fmt.Fprint(w, "1")
			}
			return
		}
		fmt.Fprint(w, `{"limitcount":10}`)
	})
	fresh := fake(t, func(w http.ResponseWriter, r *http.Request) {
		freshChecks++
		if r.URL.Path == "/User/CheckUserStatus" {
			fmt.Fprint(w, "1")
			return
		}
		fmt.Fprint(w, `{"limitcount":10}`)
	})
	primary.Session.Cookie = "UserId=same; token=old"
	fresh.Session.Cookie = "UserId=same; token=new"
	ready, err := preparePoolWithRetry(context.Background(), []*Client{primary, fresh}, sleepContext)
	if err != nil || len(ready) != 1 || ready[0].client != primary || freshChecks != 0 {
		t.Fatal(err, freshChecks)
	}
	expired = true
	ready, err = preparePoolWithRetry(context.Background(), []*Client{primary, fresh}, sleepContext)
	if err != nil || len(ready) != 1 || ready[0].client != fresh || freshChecks != 2 {
		t.Fatal(err, freshChecks)
	}
}
