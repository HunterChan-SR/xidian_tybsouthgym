// 放票时钟与串行调度；成功或未知结果结束整个账号池。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

func targetDate(cfg Config, now time.Time) string {
	return now.In(shanghai).AddDate(0, 0, cfg.DateAdd).Format("2006-01-02")
}

func window(cfg Config, now time.Time) (time.Time, time.Time) {
	now = now.In(shanghai)
	t, _ := time.Parse("15:04:05", cfg.ReleaseTime)
	start := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, shanghai)
	return start, start.Add(time.Duration(cfg.WindowSeconds) * time.Second)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func runPool(ctx context.Context, clients []*Client, cfg Config, mode string) (out Outcome, runErr error) {
	now := time.Now().In(shanghai)
	out = Outcome{Status: "started", At: now.Format(time.RFC3339), TargetDate: targetDate(cfg, now)}
	if (mode == "book" || mode == "test-book" || mode == "cancel-test") && !cfg.AllowLive {
		return out, errors.New("真实预约尚未在配置中启用")
	}
	var prepare = preparePool
	if mode == "book" || mode == "watch" {
		_, latestEnd := window(cfg, now)
		if !now.Before(latestEnd.Add(10 * time.Second)) {
			return out, &Fault{Kind: "missed_window"}
		}
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, latestEnd.Add(10*time.Second))
		defer stop()
		prepare = func(ctx context.Context, clients []*Client) ([]poolMember, error) {
			return preparePoolWithRetry(ctx, clients, sleepContext)
		}
	}
	ready, e := prepare(ctx, clients)
	if e != nil {
		return out, e
	}
	c := ready[0].client
	limit := ready[0].limit

	if cfg.TestOnly && cfg.ExactDate != out.TargetDate && mode != "cancel-test" {
		return out, &Fault{Kind: "test_authorization_expired"}
	}
	if mode == "book" && cfg.TestOnly || mode == "test-book" && !cfg.TestOnly {
		return out, &Fault{Kind: "wrong_mode_for_config"}
	}
	if mode == "cancel-test" {
		var saved Outcome
		b, err := os.ReadFile(receiptPath(cfg, c.Session, cfg.ExactDate))
		if err != nil {
			return out, err
		}
		if json.Unmarshal(b, &saved) != nil {
			return out, &Fault{Kind: "invalid_test_receipt"}
		}
		return cancelTest(ctx, c, cfg, saved)
	}
	// Import-heavy external workers are made ready before waiting for release.
	// Read-only inventory modes do not start a solver or fetch any challenge.
	if mode == "book" || mode == "test-book" {
		closeWorker, err := attachOracleWorker(ctx, clients, cfg)
		if err != nil {
			return out, err
		}
		defer closeWorker()
	}
	start, end := window(cfg, now)
	if mode != "probe" && mode != "test-book" {
		offset, err := c.clockOffset()
		if err != nil {
			return out, err
		}
		trigger := start.Add(-offset)
		deadline := end.Add(-offset)
		if !time.Now().Before(deadline) {
			return out, &Fault{Kind: "missed_window"}
		}
		if time.Until(trigger) > 10*time.Minute {
			return out, &Fault{Kind: "too_early_start_within_10_minutes"}
		}
		if time.Until(trigger) > 5*time.Second {
			fmt.Println("登录检查通过，等待北京时间", start.Format("15:04:05"))
			if err = sleepContext(ctx, time.Until(trigger.Add(-5*time.Second))); err != nil {
				return out, err
			}
			ready, err = prepare(ctx, clients)
			if err != nil {
				return out, err
			}
			c = ready[0].client
			offset, err = c.clockOffset()
			if err != nil {
				return out, err
			}
			trigger = start.Add(-offset)
			deadline = end.Add(-offset)
		}
		c.event(map[string]any{"event": "schedule", "at": time.Now().In(shanghai).Format(time.RFC3339Nano), "estimatedServerOffsetMs": offset.Milliseconds(), "localTrigger": trigger.Format(time.RFC3339Nano)})
		if err = sleepContext(ctx, time.Until(trigger)); err != nil {
			return out, err
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	// A protocol/price anomaly latches the entire pool into read-only observation.
	// A later normal response must never silently re-enable submissions.
	observationReason := ""
	degrade := func(c *Client, err error) {
		if observationReason == "" {
			observationReason = faultKind(err)
			c.event(map[string]any{"event": "observation_only", "at": time.Now().In(shanghai).Format(time.RFC3339Nano), "account": c.Name, "reason": observationReason})
		}
	}
	defer func() {
		if observationReason != "" {
			out.ObservationOnlyReason = observationReason
			if errors.Is(runErr, context.DeadlineExceeded) {
				runErr = &Fault{Kind: "observation_only_window_closed"}
			}
		}
	}()
	orderFailures := 0
	cursor := 0
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		index := cursor % len(ready)
		cursor++
		member := &ready[index]
		if member.disabled {
			allDisabled := true
			for _, m := range ready {
				if !m.disabled {
					allDisabled = false
				}
			}
			if allDisabled {
				return out, &Fault{Kind: "no_usable_accounts"}
			}
			continue
		}
		c = member.client
		limit = member.limit
		if time.Now().Before(member.next) {
			allCooling := true
			soonest := member.next
			for _, m := range ready {
				if m.disabled {
					continue
				}
				if !time.Now().Before(m.next) {
					allCooling = false
				}
				if m.next.Before(soonest) {
					soonest = m.next
				}
			}
			if allCooling {
				if err := sleepContext(ctx, time.Until(soonest)); err != nil {
					return out, err
				}
			}
			continue
		}
		slots, err := c.slots(ctx, cfg, limit)
		delay := time.Duration(cfg.PollSeconds) * time.Second
		if err != nil {
			var f *Fault
			if faultKind(err) == "login_required" && len(ready) > 1 {
				member.disabled = true
				continue
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			c.event(map[string]any{"event": "inventory_failure", "at": time.Now().In(shanghai).Format(time.RFC3339Nano), "account": c.Name, "reason": faultKind(err)})
			if mode == "probe" || mode == "test-book" || !errors.As(err, &f) {
				return out, err
			}
			if observationFault(f.Kind) {
				degrade(c, err)
			} else if f.Kind != "inventory_busy" && f.Kind != "rate_limited" && f.Kind != "server_error" && f.Kind != "network_error" && f.Kind != "date_not_released" {
				return out, err
			}
			if f.RetryAfter > delay {
				delay = f.RetryAfter
			}
			if f.Kind != "date_not_released" && delay < 5*time.Second {
				delay = 5 * time.Second
			}
		} else {
			candidates, err := choose(slots, cfg, limit)
			observation := inventoryObservation(cfg, slots, len(candidates), c.Name)
			if cfg.EveningFlexible {
				primary, primaryErr := chooseFixed(slots, cfg, limit)
				observation["fullWindowCandidates"] = len(primary)
				if primaryErr != nil {
					observation["fullWindowCandidates"] = -1
				}
				observation["flexibleWindowCandidates"] = len(candidates)
				if len(candidates) > 0 {
					observation["selectedBegin"] = candidates[0].Items[0].BeginTime
					observation["selectedEnd"] = candidates[0].Items[len(candidates[0].Items)-1].Endtime
				}
			}
			if err != nil {
				observation["selectionError"] = faultKind(err)
				observation["fullWindowCandidates"] = -1 // Unknown, not zero availability.
				if (mode == "book" || mode == "watch") && observationFault(faultKind(err)) {
					degrade(c, err)
				} else {
					c.event(observation)
					return out, err
				}
			}
			observation["observationOnly"] = observationReason != ""
			c.event(observation)
			if observationReason != "" {
				out.Status = "observation_only"
			} else if len(candidates) > 0 {
				cand := candidates[0]
				out.Status = "available"
				out.Account = c.Name
				out.FieldNo = cand.Items[0].FieldNo
				out.TotalCents = cand.TotalCents
				if mode == "book" || mode == "test-book" {
					if time.Now().In(shanghai).Format("2006-01-02") != now.Format("2006-01-02") {
						return out, &Fault{Kind: "date_changed"}
					}
					selectedConfig := cfg
					selectedConfig.BeginTime = cand.Items[0].BeginTime
					selectedConfig.EndTime = cand.Items[len(cand.Items)-1].Endtime
					result, submitErr := guardedSubmit(ctx, c, selectedConfig, cand, out.TargetDate)
					if submitErr == nil {
						if mode == "test-book" {
							return cancelTest(ctx, c, cfg, result)
						}
						if verifyErr := c.verifyOrder(ctx, selectedConfig, result); verifyErr != nil {
							return result, verifyErr
						}
						return result, nil
					}
					if mode == "test-book" {
						return result, submitErr
					}
					kind := faultKind(submitErr)
					if kind != "slot_taken" && kind != "order_rate_limited" && kind != "order_busy" && kind != "date_not_released" {
						return result, submitErr
					}
					out = result
					if kind == "order_rate_limited" {
						member.next = time.Now().Add(30 * time.Second)
					}
					orderFailures++
					retryDelay := time.Duration(cfg.PollSeconds) * time.Second
					if kind == "order_rate_limited" || kind == "order_busy" {
						retryDelay = 5 * time.Second * time.Duration(1<<min(orderFailures-1, 2))
					}
					if e := sleepContext(ctx, retryDelay); e != nil {
						return out, e
					}
					continue
				}
				return out, nil
			}
			if observationReason == "" {
				out.Status = "no_matching_slots"
			}
			if mode == "probe" || mode == "test-book" {
				return out, nil
			}
		}
		if observationReason != "" && delay < 5*time.Second {
			delay = 5 * time.Second
		}
		if e = sleepContext(ctx, delay); e != nil {
			return out, e
		}
	}
}

func run(ctx context.Context, c *Client, cfg Config, mode string) (Outcome, error) {
	return runPool(ctx, []*Client{c}, cfg, mode)
}

func (c *Client) clockOffset() (time.Duration, error) {
	if len(c.ClockOffsets) == 0 {
		return 0, nil
	}
	samples := append([]time.Duration(nil), c.ClockOffsets...)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	offset := samples[len(samples)/2]
	if offset > 10*time.Second || offset < -10*time.Second {
		return 0, &Fault{Kind: "clock_skew_too_large"}
	}
	return offset, nil
}

// Only read/selection faults can reach this policy. Submission faults always use
// guardedSubmit's stop-and-reconcile path, including timeouts and unknown replies.
func observationFault(kind string) bool {
	switch kind {
	case "schema_changed", "date_mapping_changed", "price_missing_or_invalid", "duplicate_slot", "unexpected_response", "venue_time_error", "http_error", "response_too_large":
		return true
	}
	return false
}
