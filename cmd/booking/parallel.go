// Two in-flight attempts, one durable campaign, and a bounded reconciliation tail.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type raceAttempt struct {
	Account   string
	Identity  string
	Candidate Candidate
	State     string
	Result    Outcome
	Error     string
}
type raceJournal struct {
	Outcome
	Schema   int
	Winner   int
	Attempts []raceAttempt
}
type raceReply struct {
	index int
	out   Outcome
	err   error
}

func attemptConfig(cfg Config, date string, index int, candidate Candidate) Config {
	cfg.StateDir = filepath.Join(cfg.StateDir, "parallel", date, fmt.Sprintf("attempt-%d", index+1))
	cfg.BeginTime = candidate.Items[0].BeginTime
	cfg.EndTime = candidate.Items[len(candidate.Items)-1].Endtime
	return cfg
}

// Validate persisted candidates too: recovery must not cancel unrelated orders.
func raceCandidateOK(cfg Config, cand Candidate, date string, limit int) bool {
	day, err := time.Parse("2006-01-02", date)
	if err != nil || len(cand.Items) != 2 || limit < 2 || cand.TotalCents < 0 || cand.TotalCents > 2400 || cand.TotalCents > cfg.MaxTotalCents {
		return false
	}
	begin, end := cand.Items[0].BeginTime, cand.Items[1].Endtime
	allowed := begin == "19:00" && end == "21:00"
	if cfg.WeekendAfternoon && (day.Weekday() == time.Saturday || day.Weekday() == time.Sunday) {
		allowed = allowed || begin == "15:00" && end == "17:00"
	}
	if !allowed {
		return false
	}
	total := 0
	for i, item := range cand.Items {
		b, e1 := minutes(item.BeginTime)
		e, e2 := minutes(item.Endtime)
		price, pe := money(item.Price)
		if e1 != nil || e2 != nil || pe != nil || price < 0 || e-b != 60 || item.FieldTypeNo != "001" || item.FieldNo != cand.Items[0].FieldNo {
			return false
		}
		if i > 0 && cand.Items[i-1].Endtime != item.BeginTime {
			return false
		}
		total += price
	}
	test := cfg
	test.BeginTime = begin
	test.EndTime = end
	test.WeekendAfternoon = false
	slots := []Slot{}
	for _, item := range cand.Items {
		price, _ := json.Marshal(item.Price)
		slots = append(slots, Slot{FieldNo: item.FieldNo, FieldName: item.FieldName, FieldTypeNo: item.FieldTypeNo, BeginTime: item.BeginTime, EndTime: item.Endtime, FinalPrice: price, FieldState: json.RawMessage("0"), TimeStatus: json.RawMessage("1")})
	}
	selected, err := chooseFixed(slots, test, limit)
	return err == nil && len(selected) > 0 && total == cand.TotalCents
}

func runTwoCandidates(ctx context.Context, ready []poolMember, cfg Config, candidates []Candidate, date string) (Outcome, error) {
	if cfg.ParallelOrders != 2 || cfg.TestOnly || !cfg.AllowLive || cfg.Validate() != nil {
		return Outcome{}, &Fault{Kind: "parallel_not_authorized"}
	}
	path := receiptPath(cfg, Session{}, date)
	if _, err := os.Stat(path); err == nil {
		return Outcome{}, &Fault{Kind: "existing_order_or_uncertain_attempt"}
	} else if !os.IsNotExist(err) {
		return Outcome{}, err
	}
	journal := raceJournal{Outcome: Outcome{Status: "parallel_active", TargetDate: date, At: time.Now().In(shanghai).Format(time.RFC3339Nano), FieldTypeNo: cfg.FieldTypeNo}, Schema: 1, Winner: -1}
	if err := saveJSON(path, journal); err != nil {
		return journal.Outcome, err
	}
	replies := make(chan raceReply, 2)
	flight, nextAccount, nextCandidate := 0, 0, 0
	stopped := false
	var persistErr error
	nextLaunch := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	// Dispatch never exceeds two pending/possibly-held attempts or three identities.
	// A success/unknown stops new launches but does not cancel already sent requests.
	for {
		if ctx.Err() != nil {
			stopped = true
		}
		for nextAccount < len(ready) && ready[nextAccount].limit < 2 {
			nextAccount++
		}
		canLaunch := !stopped && flight < 2 && nextAccount < len(ready) && nextAccount < 3 && nextCandidate < len(candidates)
		if !canLaunch && flight == 0 {
			break
		}
		var tick <-chan time.Time
		if canLaunch {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(max(time.Duration(0), time.Until(nextLaunch)))
			tick = timer.C
		}
		var cancelled <-chan struct{}
		if !stopped {
			cancelled = ctx.Done()
		}
		select {
		case <-cancelled:
			stopped = true
		case <-tick:
			// Prefer an already queued result over sending another request.
			select {
			case r := <-replies:
				flight--
				recordRaceReply(&journal, r)
				if r.err == nil || faultKind(r.err) != "slot_taken" {
					stopped = true
				} else {
					nextLaunch = time.Now().Add(time.Duration(cfg.PollSeconds) * time.Second)
				}
				if err := saveJSON(path, journal); err != nil {
					persistErr = err
					stopped = true
				}
				continue
			default:
			}
			if ctx.Err() != nil {
				stopped = true
				continue
			}
			member := ready[nextAccount]
			candidate := candidates[nextCandidate]
			if !raceCandidateOK(cfg, candidate, date, member.limit) {
				stopped = true
				persistErr = &Fault{Kind: "parallel_candidate_invalid"}
				continue
			}
			// Save intent before starting the worker; a crash remains visibly unresolved.
			index := len(journal.Attempts)
			journal.Attempts = append(journal.Attempts, raceAttempt{Account: member.client.Name, Identity: accountKey(member.client.Session), Candidate: candidate, State: "pending"})
			if err := saveJSON(path, journal); err != nil {
				persistErr = err
				stopped = true
				continue
			}
			nextAccount++
			nextCandidate++
			flight++
			member.client.event(map[string]any{"event": "parallel_dispatch", "account": member.client.Name, "attempt": index + 1, "inFlight": flight, "inFlightLimit": 2, "temporaryCapCents": 4800, "date": date})
			nextLaunch = time.Now().Add(time.Duration(cfg.ParallelStaggerMillis) * time.Millisecond)
			child := attemptConfig(cfg, date, index, candidate)
			go func(index int, c *Client, child Config, candidate Candidate) {
				out, err := guardedSubmit(ctx, c, child, candidate, date)
				if err != nil && out.Status == "pending" {
					if _, statErr := os.Stat(receiptPath(child, c.Session, date)); os.IsNotExist(statErr) {
						out.Status = "not_submitted"
					}
				}
				replies <- raceReply{index, out, err}
			}(index, member.client, child, candidate)
		case r := <-replies:
			flight--
			recordRaceReply(&journal, r)
			if r.err == nil || faultKind(r.err) != "slot_taken" {
				stopped = true
			} else {
				nextLaunch = time.Now().Add(time.Duration(cfg.PollSeconds) * time.Second)
			}
			if err := saveJSON(path, journal); err != nil {
				persistErr = err
				stopped = true
			}
		}
	}
	if persistErr != nil {
		journal.Status = "parallel_reconciliation_required"
		return journal.Outcome, persistErr
	}
	clients := []*Client{}
	for _, member := range ready {
		clients = append(clients, member.client)
	}
	// Cleanup never extends the submission window and never creates another order.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return reconcileRace(cleanup, clients, cfg, &journal, path)
}

func recordRaceReply(j *raceJournal, r raceReply) {
	a := &j.Attempts[r.index]
	a.Result = r.out
	switch {
	case r.err == nil:
		a.State = "success"
		if j.Winner < 0 {
			j.Winner = r.index
		}
	case r.out.Status == "rejected":
		a.State = "rejected"
		a.Error = faultKind(r.err)
	case r.out.Status == "uncertain" || r.out.Status == "reserved_payment_manual" || r.out.Status == "pending":
		// A local write failure after network IO is not evidence of rejection.
		a.State = "unknown"
		a.Error = faultKind(r.err)
	default:
		a.State = "not_submitted"
		a.Error = faultKind(r.err)
	}
}

func reconcileRace(ctx context.Context, clients []*Client, cfg Config, j *raceJournal, path string) (Outcome, error) {
	if j.Schema != 1 || len(j.Attempts) > 3 {
		return j.Outcome, &Fault{Kind: "parallel_journal_invalid"}
	}
	byName := map[string]*Client{}
	for _, c := range clients {
		byName[c.Name] = c
	}
	unresolved := false
	verified := map[int]*Client{}
	for i := range j.Attempts {
		a := &j.Attempts[i]
		if !raceCandidateOK(cfg, a.Candidate, j.TargetDate, 2) {
			return j.Outcome, &Fault{Kind: "parallel_journal_invalid"}
		}
		if a.State == "rejected" || a.State == "not_submitted" || a.State == "cancelled" {
			continue
		}
		c := byName[a.Account]
		if c == nil || accountKey(c.Session) != a.Identity {
			unresolved = true
			continue
		}
		if a.Result.OrderID == "" {
			unresolved = true
			continue
		}
		child := attemptConfig(cfg, j.TargetDate, i, a.Candidate)
		if a.Result.Account != a.Account || a.Result.TargetDate != j.TargetDate || a.Result.FieldNo != a.Candidate.Items[0].FieldNo || a.Result.TotalCents != a.Candidate.TotalCents {
			unresolved = true
			continue
		}
		if err := c.verifyOrder(ctx, child, a.Result); err != nil {
			a.Error = faultKind(err)
			unresolved = true
			continue
		}
		status, err := c.payStatus(ctx, a.Result.OrderID)
		if err != nil {
			a.Error = faultKind(err)
			unresolved = true
			continue
		}
		if status == "4" {
			a.State = "cancelled"
			continue
		}
		if a.State == "cancel_pending" || a.State == "cancel_unknown" {
			unresolved = true
			continue
		}
		if status != "0" && status != "1" {
			unresolved = true
			continue
		}
		a.State = "verified"
		verified[i] = c
	}
	// Prefer the first acknowledged success if it still verifies. A persisted
	// keeper is stable across restarts; never swap it for a later response.
	winner := j.Winner
	if _, ok := verified[winner]; !ok {
		winner = -1
		for i := range j.Attempts {
			if _, ok := verified[i]; ok {
				winner = i
				break
			}
		}
	}
	j.Winner = winner
	if winner >= 0 {
		savedDate := j.TargetDate
		j.Outcome = j.Attempts[winner].Result
		j.TargetDate = savedDate
		j.Status = "parallel_reconciling"
		if err := saveJSON(path, j); err != nil {
			return j.Outcome, err
		}
		for i, c := range verified {
			if i == winner {
				continue
			}
			a := &j.Attempts[i]
			status, err := c.payStatus(ctx, a.Result.OrderID)
			if err != nil || status != "0" {
				unresolved = true
				a.Error = "duplicate_not_unpaid"
				continue
			}
			a.State = "cancel_pending"
			if err := saveJSON(path, j); err != nil {
				return j.Outcome, err
			}
			// One cancellation request. Its response alone cannot release the holding.
			_, _ = c.get(ctx, "/Field/FieldCancel", url.Values{"OID": {a.Result.OrderID}})
			status, err = c.payStatus(ctx, a.Result.OrderID)
			if err != nil || status != "4" {
				a.State = "cancel_unknown"
				unresolved = true
			} else {
				a.State = "cancelled"
			}
			if err := saveJSON(path, j); err != nil {
				return j.Outcome, err
			}
		}
	}
	if unresolved {
		j.Status = "parallel_reconciliation_required"
		j.Detail = "保留所有订单保护；核对每个账号订单及取消最终状态，不重新提交"
	} else if winner >= 0 {
		j.Status = "reserved_payment_manual"
		j.Detail = "两路结果已收齐，多余订单已核验取消，仅保留这一笔供手动支付"
	} else {
		j.Status = "parallel_no_reservation"
	}
	if err := saveJSON(path, j); err != nil {
		return j.Outcome, err
	}
	if len(clients) > 0 {
		clients[0].event(map[string]any{"event": "parallel_settled", "status": j.Status, "attempts": len(j.Attempts), "winner": j.Winner, "date": j.TargetDate})
	}
	if unresolved {
		return j.Outcome, &Fault{Kind: "parallel_reconciliation_required"}
	}
	return j.Outcome, nil
}

// Restart recovery reads known per-attempt receipts and reconciles only; pending
// intents without an order ID stay blocked for official order-list inspection.
func recoverParallel(ctx context.Context, clients []*Client, cfg Config, date string) (Outcome, error) {
	if cfg.ParallelOrders != 2 || !cfg.AllowLive || cfg.Validate() != nil {
		return Outcome{}, &Fault{Kind: "parallel_not_authorized"}
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return Outcome{}, &Fault{Kind: "parallel_date_required"}
	}
	path := receiptPath(cfg, Session{}, date)
	data, err := os.ReadFile(path)
	if err != nil {
		return Outcome{}, err
	}
	var j raceJournal
	if json.Unmarshal(data, &j) != nil || j.TargetDate != date || j.Schema != 1 {
		return Outcome{}, &Fault{Kind: "parallel_journal_invalid"}
	}
	for i := range j.Attempts {
		a := &j.Attempts[i]
		if !raceCandidateOK(cfg, a.Candidate, date, 2) {
			return j.Outcome, &Fault{Kind: "parallel_journal_invalid"}
		}
		if a.State != "pending" {
			continue
		}
		child := attemptConfig(cfg, date, i, a.Candidate)
		data, err := os.ReadFile(receiptPath(child, Session{}, date))
		var out Outcome
		if err == nil && json.Unmarshal(data, &out) == nil {
			if out.Status == "reserved_payment_manual" {
				recordRaceReply(&j, raceReply{i, out, nil})
			} else {
				a.Result = out
				a.State = "unknown"
			}
		} else {
			a.State = "unknown"
		}
	}
	cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return reconcileRace(cleanup, clients, cfg, &j, path)
}
