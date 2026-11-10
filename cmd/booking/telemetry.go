// 脱敏事件与目标库存证据；不保存原始会员数据。
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var eventWriteMu sync.Mutex

func (c *Client) event(value any) {
	eventWriteMu.Lock()
	defer eventWriteMu.Unlock()
	if c.EventLog == "" {
		return
	}
	// Events without their own request-start timestamp still need an exact time
	// to reconstruct inventory -> oracle -> durable guard -> submission latency.
	if record, ok := value.(map[string]any); ok {
		if _, present := record["at"]; !present {
			record["at"] = time.Now().In(shanghai).Format(time.RFC3339Nano)
		}
	}
	b, e := json.Marshal(value)
	if e != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(c.EventLog), 0700) != nil {
		return
	}
	f, e := os.OpenFile(c.EventLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func (c *Client) redact(message string) string {
	for _, part := range strings.Split(c.Session.Cookie, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && len(kv[1]) >= 8 {
			message = strings.ReplaceAll(message, kv[1], "[redacted]")
		}
	}
	runes := []rune(message)
	if len(runes) > 500 {
		message = string(runes[:500])
	}
	return message
}

// Record target-window states as well as availability. Raw member data and
// unrecognized state strings never enter the event log.
func inventoryObservation(cfg Config, slots []Slot, fullCandidates int, account string) map[string]any {
	rows := []map[string]any{}
	targetRows := []map[string]any{}
	states := map[string]int{}
	begin, _ := minutes(cfg.BeginTime)
	end, _ := minutes(cfg.EndTime)
	if cfg.EveningFlexible {
		begin, end = 18*60, 22*60
	}
	if cfg.AllDayFallback {
		begin, end = 0, 1440
	}
	if weekendAfternoon(cfg, time.Now()) {
		begin = 15 * 60
		end = 21 * 60
	}
	unknownPrices, unavailable := 0, 0
	for _, s := range slots {
		b, be := minutes(s.BeginTime)
		e, ee := minutes(s.EndTime)
		if be != nil || ee != nil || b < begin || e > end || e <= b || s.FieldTypeNo != cfg.FieldTypeNo || !strings.HasPrefix(s.FieldNo, cfg.FieldPrefix) {
			continue
		}
		if weekendAfternoon(cfg, time.Now()) && !((b >= 15*60 && e <= 17*60) || (b >= 19*60 && e <= 21*60)) {
			continue
		}
		fieldState, timeStatus := stateCode(s.FieldState), stateCode(s.TimeStatus)
		states["fieldState="+fieldState+";timeStatus="+timeStatus]++
		row := map[string]any{"field": s.FieldNo, "begin": s.BeginTime, "end": s.EndTime, "fieldState": fieldState, "timeStatus": timeStatus}
		price, err := money(atom(s.FinalPrice))
		if err == nil {
			row["priceCents"] = price
		}
		targetRows = append(targetRows, row)
		if atom(s.FieldState) != "0" || atom(s.TimeStatus) != "1" {
			unavailable++
			continue
		}
		if err != nil {
			unknownPrices++
			continue
		}
		rows = append(rows, map[string]any{"field": s.FieldNo, "begin": s.BeginTime, "end": s.EndTime, "priceCents": price})
	}
	return map[string]any{"event": "inventory_observation", "at": time.Now().In(shanghai).Format(time.RFC3339Nano), "account": account, "date": targetDate(cfg, time.Now()), "fieldType": cfg.FieldTypeNo, "begin": cfg.BeginTime, "end": cfg.EndTime, "maxTotalCents": cfg.MaxTotalCents, "fullWindowCandidates": fullCandidates, "unknownPrices": unknownPrices, "availableSlots": rows, "targetSlots": targetRows, "targetSlotCount": len(targetRows), "unavailableSlotCount": unavailable, "stateCounts": states}
}

func stateCode(raw json.RawMessage) string {
	s := atom(raw)
	if len(s) == 0 || len(s) > 3 {
		return "unknown"
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "unknown"
		}
	}
	return s
}

func jsonShape(raw []byte) string {
	if len(raw) == 0 {
		return "missing"
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "invalid_json"
	}
	switch value.(type) {
	case nil:
		return "null"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case string:
		return "string"
	case bool:
		return "boolean"
	default:
		return "number"
	}
}

// Save an allowlisted structural summary, never the response body or arbitrary keys.
func (c *Client) readFailure(endpoint string, body []byte, err error) {
	record := map[string]any{"event": "read_diagnostic", "at": time.Now().In(shanghai).Format(time.RFC3339Nano), "account": c.Name, "endpoint": endpoint, "reason": faultKind(err), "bodyShape": jsonShape(body), "bodyBytes": len(body)}
	var f *Fault
	if errors.As(err, &f) && f.Reason != "" {
		record["parseReason"] = f.Reason
	}
	var env Envelope
	if json.Unmarshal(body, &env) == nil {
		record["businessType"] = stateCode(env.Type)
		record["resultShape"] = jsonShape(env.ResultData)
		if jsonShape(env.ResultData) == "string" {
			record["decodedResultShape"] = jsonShape([]byte(atom(env.ResultData)))
		}
	}
	c.event(record)
}
