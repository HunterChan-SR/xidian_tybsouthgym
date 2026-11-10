package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

type raceMock struct {
	mu              sync.Mutex
	active, peak    int
	orders, cancels []string
	status          map[string]string
	items           map[string][]Item
	behavior        string
	bEntered        chan struct{}
	releaseA        chan struct{}
}

func raceFixture(t *testing.T, behavior string) (*raceMock, []poolMember, Config, []Candidate) {
	t.Helper()
	m := &raceMock{status: map[string]string{}, items: map[string][]Item{}, behavior: behavior, bEntered: make(chan struct{}), releaseA: make(chan struct{})}
	cfg := config(t)
	cfg.PoolID = "race-test"
	cfg.ParallelOrders = 2
	cfg.ParallelStaggerMillis = 50
	cfg.MaxTemporaryCents = 4800
	date := targetDate(cfg, time.Now())
	handler := func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("UserId")
		name := ""
		if cookie != nil {
			name = cookie.Value
		}
		oid := r.URL.Query().Get("OID")
		switch r.URL.Path {
		case "/Field/OrderField":
			var items []Item
			json.Unmarshal([]byte(r.URL.Query().Get("checkdata")), &items)
			m.mu.Lock()
			m.active++
			if m.active > m.peak {
				m.peak = m.active
			}
			m.orders = append(m.orders, name)
			m.items["oid-"+name] = items
			m.status["oid-"+name] = "0"
			m.mu.Unlock()
			defer func() { m.mu.Lock(); m.active--; m.mu.Unlock() }()
			if name == "b" {
				close(m.bEntered)
			}
			if name == "a" && behavior != "fast" {
				select {
				case <-m.bEntered:
				case <-time.After(time.Second):
					t.Error("B waited for A")
				}
				if behavior == "cancel-context" {
					<-m.releaseA
				} else {
					time.Sleep(70 * time.Millisecond)
				}
			}
			if name == "a" && behavior == "reject" {
				fmt.Fprint(w, `{"type":2,"message":"场地已被预订"}`)
				return
			}
			if name == "b" && behavior == "reject" {
				time.Sleep(2300 * time.Millisecond)
			}
			if name == "a" && (behavior == "lost" || behavior == "cancel-context") {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			if name == "a" && behavior == "rate" {
				fmt.Fprint(w, `{"type":2,"message":"下单速度过快"}`)
				return
			}
			fmt.Fprintf(w, `{"type":1,"resultdata":"oid-%s"}`, name)
		case "/Field/GetPayInfo":
			m.mu.Lock()
			items := m.items[oid]
			m.mu.Unlock()
			rows := []map[string]string{}
			for _, item := range items {
				rows = append(rows, map[string]string{"FieldNo": item.FieldNo, "OrderDate": date, "BeginTime": item.BeginTime, "EndTime": item.Endtime, "Price": item.Price})
			}
			json.NewEncoder(w).Encode(rows)
		case "/Field/CheckStatus":
			m.mu.Lock()
			s := m.status[oid]
			m.mu.Unlock()
			fmt.Fprintf(w, `[{"PayStatus":"%s"}]`, s)
		case "/Field/FieldCancel":
			m.mu.Lock()
			m.cancels = append(m.cancels, oid)
			if behavior != "cancel-fail" {
				m.status[oid] = "4"
			}
			m.mu.Unlock()
			if behavior == "cancel-lost" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			fmt.Fprint(w, `{"type":1}`)
		default:
			t.Error("unexpected HTTP", r.URL.Path)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	ready := []poolMember{}
	for _, name := range []string{"a", "b", "c"} {
		client := newClient(Session{Cookie: "UserId=" + name}, 5*time.Second)
		client.Name = name
		client.Base = server.URL
		u, _ := url.Parse(server.URL)
		client.HTTP.Jar.SetCookies(u, []*http.Cookie{{Name: "UserId", Value: name}})
		t.Cleanup(client.HTTP.CloseIdleConnections)
		ready = append(ready, poolMember{client: client, limit: 10})
	}
	slots := append(pair("YMQ004", "12"), pair("YMQ001", "12")...)
	slots = append(slots, pair("YMQ002", "12")...)
	candidates, err := choose(slots, cfg, 10)
	if err != nil {
		t.Fatal(err)
	}
	return m, ready, cfg, candidates
}

func TestTwoLaneDrainsLateSuccessAndCancelsOnlyExtra(t *testing.T) {
	for _, behavior := range []string{"late", "cancel-lost"} {
		t.Run(behavior, func(t *testing.T) {
			m, ready, cfg, candidates := raceFixture(t, behavior)
			date := targetDate(cfg, time.Now())
			out, err := runTwoCandidates(context.Background(), ready, cfg, candidates, date)
			if err != nil || out.Account != "b" || out.Status != "reserved_payment_manual" {
				t.Fatal(out, err)
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.peak != 2 || len(m.orders) != 2 || len(m.cancels) != 1 || m.cancels[0] != "oid-a" || m.status["oid-a"] != "4" || m.status["oid-b"] != "0" {
				t.Fatal(m.peak, m.orders, m.cancels, m.status)
			}
			_, err = runTwoCandidates(context.Background(), ready, cfg, candidates, date)
			if faultKind(err) != "existing_order_or_uncertain_attempt" {
				t.Fatal(err)
			}
		})
	}
}

func TestTwoLaneUnknownStopsThirdAndKeepsEvidence(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "lost")
	out, err := runTwoCandidates(context.Background(), ready, cfg, candidates, targetDate(cfg, time.Now()))
	if faultKind(err) != "parallel_reconciliation_required" || out.Account != "b" {
		t.Fatal(out, err)
	}
	if len(m.orders) != 2 || len(m.cancels) != 0 {
		t.Fatal(m.orders, m.cancels)
	}
	data, _ := os.ReadFile(receiptPath(cfg, Session{}, out.TargetDate))
	var j raceJournal
	json.Unmarshal(data, &j)
	if len(j.Attempts) != 2 || j.Attempts[0].State != "unknown" {
		t.Fatal(j)
	}
}

func TestTwoLaneCancellationFailureRemainsBlockedAndRecoveryDoesNotRepeat(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "cancel-fail")
	date := targetDate(cfg, time.Now())
	_, err := runTwoCandidates(context.Background(), ready, cfg, candidates, date)
	if faultKind(err) != "parallel_reconciliation_required" {
		t.Fatal(err)
	}
	clients := []*Client{ready[0].client, ready[1].client, ready[2].client}
	_, err = recoverParallel(context.Background(), clients, cfg, date)
	if faultKind(err) != "parallel_reconciliation_required" || len(m.cancels) != 1 {
		t.Fatal(err, m.cancels)
	}
	m.mu.Lock()
	m.status["oid-a"] = "4"
	m.mu.Unlock()
	out, err := recoverParallel(context.Background(), clients, cfg, date)
	if err != nil || out.Account != "b" || len(m.cancels) != 1 || len(m.orders) != 2 {
		t.Fatal(out, err)
	}
}

func TestTwoLaneFastSuccessDoesNotDispatchB(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "fast")
	out, err := runTwoCandidates(context.Background(), ready, cfg, candidates, targetDate(cfg, time.Now()))
	if err != nil || out.Account != "a" || len(m.orders) != 1 || len(m.cancels) != 0 {
		t.Fatal(out, err, m.orders, m.cancels)
	}
}

func TestTwoLaneThirdReplacesOnlyExplicitOccupied(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "reject")
	out, err := runTwoCandidates(context.Background(), ready, cfg, candidates, targetDate(cfg, time.Now()))
	if err != nil || out.Account != "c" || len(m.orders) != 3 || m.peak > 2 {
		t.Fatal(out, err, m.orders, m.peak)
	}
}

func TestTwoLaneRateLimitDoesNotDispatchThird(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "rate")
	_, err := runTwoCandidates(context.Background(), ready, cfg, candidates, targetDate(cfg, time.Now()))
	if err != nil || len(m.orders) != 2 {
		t.Fatal(err, m.orders)
	}
}

func TestTwoLaneDeadlineDrainsWithoutNewSubmission(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "cancel-context")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-m.bEntered; cancel(); close(m.releaseA) }()
	out, err := runTwoCandidates(ctx, ready, cfg, candidates, targetDate(cfg, time.Now()))
	if faultKind(err) != "parallel_reconciliation_required" {
		t.Fatal(out, err)
	}
	if len(m.orders) != 2 {
		t.Fatal(m.orders)
	}
}

func TestTwoLaneRejectsExpandedScope(t *testing.T) {
	_, _, cfg, candidates := raceFixture(t, "fast")
	for _, mutate := range []func(*Config){
		func(c *Config) { c.ParallelOrders = 3 }, func(c *Config) { c.MaxTemporaryCents = 7200 }, func(c *Config) { c.MaxTotalCents = 2500 }, func(c *Config) { c.DateAdd = 1 }, func(c *Config) { c.TestOnly = true },
	} {
		copy := cfg
		mutate(&copy)
		if copy.Validate() == nil {
			t.Fatal("expanded scope accepted")
		}
	}
	bad := candidates[0]
	bad.TotalCents = 2500
	if raceCandidateOK(cfg, bad, targetDate(cfg, time.Now()), 10) {
		t.Fatal("bad price accepted")
	}
}

func TestTwoLaneRecoveryLoadsReceiptAfterCrash(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "fast")
	date := targetDate(cfg, time.Now())
	child := attemptConfig(cfg, date, 0, candidates[0])
	out, err := guardedSubmit(context.Background(), ready[0].client, child, candidates[0], date)
	if err != nil {
		t.Fatal(err)
	}
	// The per-attempt receipt persisted; the campaign crashed before recording reply.
	j := raceJournal{Outcome: Outcome{Status: "parallel_active", TargetDate: date}, Schema: 1, Winner: -1,
		Attempts: []raceAttempt{{Account: "a", Identity: accountKey(ready[0].client.Session), Candidate: candidates[0], State: "pending"}}}
	saveJSON(receiptPath(cfg, Session{}, date), j)
	recovered, err := recoverParallel(context.Background(), []*Client{ready[0].client}, cfg, date)
	if err != nil || recovered.OrderID != out.OrderID || len(m.orders) != 1 {
		t.Fatal(recovered, err, m.orders)
	}
}

func TestTwoLaneCrashBeforeReceiptNeverReissues(t *testing.T) {
	m, ready, cfg, candidates := raceFixture(t, "fast")
	date := targetDate(cfg, time.Now())
	j := raceJournal{Outcome: Outcome{Status: "parallel_active", TargetDate: date}, Schema: 1, Winner: -1,
		Attempts: []raceAttempt{{Account: "a", Identity: accountKey(ready[0].client.Session), Candidate: candidates[0], State: "pending"}}}
	saveJSON(receiptPath(cfg, Session{}, date), j)
	_, err := recoverParallel(context.Background(), []*Client{ready[0].client}, cfg, date)
	if faultKind(err) != "parallel_reconciliation_required" || len(m.orders) != 0 {
		t.Fatal(err, m.orders)
	}
}

func TestSharedOracleConcurrentRepliesStayAssociated(t *testing.T) {
	cfg := config(t)
	cfg.BookingCaptcha = true
	cfg.CaptchaSolverPersistent = true
	cfg.CaptchaSolverCommand = oracleStdioTestCommand(t, "success")
	w, err := startOracleWorker(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	var wg sync.WaitGroup
	for _, token := range []string{"account-a", "account-b", "account-c"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			request := map[string]any{"deadline": time.Now().Add(time.Second), "challenge": map[string]string{"token": token}}
			var reply captchaReply
			if err := w.exchange(context.Background(), request, &reply); err != nil || reply.Token != token {
				t.Errorf("reply mismatch: %v", err)
			}
		}(token)
	}
	wg.Wait()
}
