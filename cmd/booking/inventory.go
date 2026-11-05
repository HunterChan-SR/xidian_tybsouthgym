// 库存读取与同场连续时段选择；选择逻辑独立于调度。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func periods(begin, end string) []string {
	b, _ := minutes(begin)
	e, _ := minutes(end)
	var out []string
	for _, p := range []struct {
		start, end int
		id         string
	}{{0, 720, "0"}, {720, 1080, "1"}, {1080, 1440, "2"}} {
		if b < p.end && e > p.start {
			out = append(out, p.id)
		}
	}
	return out
}

func parseSlots(b []byte) ([]Slot, error) {
	var env Envelope
	if json.Unmarshal(b, &env) != nil {
		return nil, &Fault{Kind: "schema_changed", Reason: "invalid_envelope"}
	}
	// Observed read-only overload reply; it does not describe inventory schema.
	if atom(env.Type) == "0" && strings.TrimSpace(env.Message) == "访问人数过多，请稍后刷新重试" {
		return nil, &Fault{Kind: "inventory_busy"}
	}
	if atom(env.Type) == "2" {
		return []Slot{}, nil
	}
	if atom(env.Type) == "3" {
		return nil, &Fault{Kind: "venue_time_error"}
	}
	if atom(env.Type) != "1" {
		return nil, &Fault{Kind: "schema_changed", Reason: "unknown_business_type"}
	}
	raw := env.ResultData
	if len(raw) > 0 && raw[0] == '"' {
		raw = []byte(atom(raw))
	}
	var slots []Slot
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &slots) != nil {
		return nil, &Fault{Kind: "schema_changed", Reason: "result_not_slot_array"}
	}
	for i := range slots {
		s := slots[i]
		var timeErr error
		s.BeginTime, timeErr = normalizeTime(s.BeginTime)
		if timeErr != nil {
			return nil, &Fault{Kind: "schema_changed", Reason: "invalid_slot_time"}
		}
		s.EndTime, timeErr = normalizeTime(s.EndTime)
		if timeErr != nil {
			return nil, &Fault{Kind: "schema_changed", Reason: "invalid_slot_time"}
		}
		slots[i] = s
		if s.FieldNo == "" || s.FieldName == "" || s.FieldTypeNo == "" || len(s.FieldState) == 0 || len(s.TimeStatus) == 0 {
			return nil, &Fault{Kind: "schema_changed", Reason: "required_slot_field_missing"}
		}
		if _, e := minutes(s.BeginTime); e != nil {
			return nil, &Fault{Kind: "schema_changed", Reason: "invalid_envelope"}
		}
		if _, e := minutes(s.EndTime); e != nil {
			return nil, &Fault{Kind: "schema_changed", Reason: "invalid_envelope"}
		}
	}
	return slots, nil
}

func (c *Client) slots(ctx context.Context, cfg Config, limits ...int) ([]Slot, error) {
	if e := c.checkDate(ctx, cfg); e != nil {
		return nil, e
	}
	var all []Slot
	queryPeriods := periods(cfg.BeginTime, cfg.EndTime)
	if weekendAfternoon(cfg, time.Now()) {
		queryPeriods = []string{"1", "2"}
	}
	if cfg.AllDayFallback {
		queryPeriods = []string{"2", "1", "0"}
	}
	for index, p := range queryPeriods {
		if weekendAfternoon(cfg, time.Now()) && index > 0 {
			afternoon := cfg
			afternoon.BeginTime = "15:00"
			afternoon.EndTime = "17:00"
			limit := 10
			if len(limits) > 0 {
				limit = limits[0]
			}
			candidates, err := chooseFixed(all, afternoon, limit)
			if err != nil {
				return nil, err
			}
			if len(candidates) > 0 {
				return all, nil
			}
			if err := sleepContext(ctx, time.Duration(cfg.PollSeconds)*time.Second); err != nil {
				return nil, err
			}
		}
		if cfg.AllDayFallback && index > 0 {
			if index == 1 {
				evening := cfg
				evening.AllDayFallback = false
				limit := 10
				if len(limits) > 0 {
					limit = limits[0]
				}
				candidates, err := choose(all, evening, limit)
				if err != nil {
					return nil, err
				}
				if len(candidates) > 0 {
					return all, nil
				}
			}
			if err := sleepContext(ctx, time.Duration(cfg.PollSeconds)*time.Second); err != nil {
				return nil, err
			}
		}
		b, e := c.get(ctx, "/Field/GetVenueStateNew", url.Values{"dateadd": {strconv.Itoa(cfg.DateAdd)}, "TimePeriod": {p}, "VenueNo": {cfg.VenueNo}, "FieldTypeNo": {cfg.FieldTypeNo}})
		if e != nil {
			return nil, e
		}
		slots, e := parseSlots(b)
		if e != nil {
			c.readFailure("/Field/GetVenueStateNew", b, e)
			return nil, e
		}
		// DateBeginTime is a time template anchored to today, not the booked date.
		// GetWeek and dateadd determine the target; verify OrderDate after submission.

		all = append(all, slots...)
	}
	return all, nil
}

func northField(field string) bool {
	return field == "BXQYMQ01" || field == "BXQYMQ02" || field == "BXQYMQ03" || field == "BXQYMQ04"
}

// All preferred two-hour windows precede single-hour fallback; one pool/receipt.
func choose(slots []Slot, cfg Config, limit int) ([]Candidate, error) {
	if weekendAfternoon(cfg, time.Now()) {
		afternoon := cfg
		afternoon.BeginTime = "15:00"
		afternoon.EndTime = "17:00"
		candidates, err := chooseFixed(slots, afternoon, limit)
		if err != nil || len(candidates) > 0 {
			return candidates, err
		}
	}
	if !cfg.EveningFlexible {
		return chooseFixed(slots, cfg, limit)
	}
	for _, window := range [][2]string{{"19:00", "21:00"}, {"20:00", "22:00"}, {"18:00", "20:00"}, {"19:00", "20:00"}, {"20:00", "21:00"}, {"21:00", "22:00"}, {"18:00", "19:00"}} {
		selected := cfg
		selected.BeginTime = window[0]
		selected.EndTime = window[1]
		candidates, err := chooseFixed(slots, selected, limit)
		if err != nil || len(candidates) > 0 {
			return candidates, err
		}
	}
	if cfg.AllDayFallback {
		// Derive valid windows from returned opening hours; later starts win ties.
		starts := map[int]bool{}
		for _, s := range slots {
			if s.FieldTypeNo != cfg.FieldTypeNo || !northField(s.FieldNo) {
				continue
			}
			begin, err := minutes(s.BeginTime)
			if err == nil {
				starts[begin] = true
			}
		}
		ordered := []int{}
		for b := range starts {
			ordered = append(ordered, b)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(ordered)))
		for _, duration := range []int{120, 60} {
			for _, b := range ordered {
				end := b + duration
				if end >= 1440 {
					continue
				}
				selected := cfg
				selected.BeginTime = fmt.Sprintf("%02d:%02d", b/60, b%60)
				selected.EndTime = fmt.Sprintf("%02d:%02d", end/60, end%60)
				candidates, err := chooseFixed(slots, selected, limit)
				if err != nil || len(candidates) > 0 {
					return candidates, err
				}
			}
		}
	}
	return nil, nil
}

func chooseFixed(slots []Slot, cfg Config, limit int) ([]Candidate, error) {
	grouped := map[string]map[string]Slot{}
	for _, s := range slots {
		if !strings.HasPrefix(s.FieldNo, cfg.FieldPrefix) || s.FieldTypeNo != cfg.FieldTypeNo || (cfg.FieldTypeNo == "007" && !northField(s.FieldNo)) {
			continue
		}
		if grouped[s.FieldNo] == nil {
			grouped[s.FieldNo] = map[string]Slot{}
		}
		if _, ok := grouped[s.FieldNo][s.BeginTime]; ok {
			return nil, &Fault{Kind: "duplicate_slot"}
		}
		grouped[s.FieldNo][s.BeginTime] = s
	}
	rank := map[string]int{}
	for i, f := range cfg.PreferredFields {
		rank[f] = i
	}
	fields := []string{}
	for f := range grouped {
		if _, ok := rank[f]; ok || cfg.AllowOtherFields {
			fields = append(fields, f)
		}
	}
	sort.Slice(fields, func(i, j int) bool {
		a, ok := rank[fields[i]]
		if !ok {
			a = len(rank)
		}
		b, ok := rank[fields[j]]
		if !ok {
			b = len(rank)
		}
		if a == b {
			return fields[i] < fields[j]
		}
		return a < b
	})
	var candidates []Candidate
	targetEnd, _ := minutes(cfg.EndTime)
	for _, field := range fields {
		current := cfg.BeginTime
		cand := Candidate{}
		for current != cfg.EndTime {
			s, ok := grouped[field][current]
			if !ok || atom(s.FieldState) != "0" || atom(s.TimeStatus) != "1" {
				break
			}
			start, _ := minutes(current)
			end, _ := minutes(s.EndTime)
			if end <= start || end > targetEnd {
				break
			}
			price, e := money(atom(s.FinalPrice))
			if e != nil {
				return nil, &Fault{Kind: "price_missing_or_invalid"}
			}
			cand.Items = append(cand.Items, Item{s.FieldNo, s.FieldTypeNo, s.FieldName, s.BeginTime, s.EndTime, fmt.Sprintf("%d.%02d", price/100, price%100)})
			cand.TotalCents += price
			current = s.EndTime
			if len(cand.Items) > limit {
				break
			}
		}
		if current == cfg.EndTime && len(cand.Items) > 0 && len(cand.Items) <= limit && cand.TotalCents <= cfg.MaxTotalCents {
			candidates = append(candidates, cand)
		}
	}
	return candidates, nil
}

func (c *Client) checkDate(ctx context.Context, cfg Config) error {
	b, e := c.get(ctx, "/Field/GetWeek", url.Values{"VenueNo": {cfg.VenueNo}, "FieldTypeNo": {cfg.FieldTypeNo}})
	if e != nil {
		return e
	}
	var days []struct{ Date string }
	if json.Unmarshal(b, &days) != nil {
		err := &Fault{Kind: "schema_changed", Reason: "calendar_not_array"}
		c.readFailure("/Field/GetWeek", b, err)
		return err
	}
	if cfg.DateAdd >= len(days) {
		return &Fault{Kind: "date_not_released"}
	}
	expected := time.Now().In(shanghai).AddDate(0, 0, cfg.DateAdd).Format("01-02")
	if days[cfg.DateAdd].Date != expected {
		err := &Fault{Kind: "date_mapping_changed", Reason: "calendar_target_mismatch"}
		c.readFailure("/Field/GetWeek", b, err)
		return err
	}
	return nil
}

func weekendAfternoon(cfg Config, now time.Time) bool {
	day := now.In(shanghai).AddDate(0, 0, cfg.DateAdd).Weekday()
	return cfg.WeekendAfternoon && (day == time.Saturday || day == time.Sunday)
}
