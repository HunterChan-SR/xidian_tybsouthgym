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

func TestChoosePreferenceBudgetContiguity(t *testing.T) {
	cfg := config(t)
	all := append(pair("YMQ001", "12.00"), pair("YMQ004", "12.00")...)
	cs, e := choose(all, cfg, 10)
	if e != nil || len(cs) != 2 || cs[0].Items[0].FieldNo != "YMQ004" || cs[0].TotalCents != 2400 {
		t.Fatalf("%+v %v", cs, e)
	}
	all[3].FieldState = json.RawMessage("1")
	cs, e = choose(all, cfg, 10)
	if e != nil || len(cs) != 1 || cs[0].Items[0].FieldNo != "YMQ001" {
		t.Fatal(cs, e)
	}
	cs, e = choose(pair("YMQ004", "12.01"), cfg, 10)
	if e != nil || len(cs) != 0 {
		t.Fatal("budget not enforced", cs, e)
	}
	cs, e = choose(pair("YMQ004", "12"), cfg, 1)
	if e != nil || len(cs) != 0 {
		t.Fatal("platform limit", cs, e)
	}
	cfg.AllowOtherFields = false
	cs, e = choose(pair("YMQ001", "12"), cfg, 10)
	if e != nil || len(cs) != 0 {
		t.Fatal("fallback forbidden", cs, e)
	}
	broken := pair("YMQ004", "12")
	broken[1].BeginTime = "20:30"
	cs, e = choose(broken, cfg, 10)
	if e != nil || len(cs) != 0 {
		t.Fatal("gap accepted", cs, e)
	}
}

func TestPriceMissingAndDuplicates(t *testing.T) {
	cfg := config(t)
	s := pair("YMQ004", "12")
	s[0].FinalPrice = nil
	if _, e := choose(s, cfg, 10); faultKind(e) != "price_missing_or_invalid" {
		t.Fatal(e)
	}
	s = pair("YMQ004", "12")
	s = append(s, s[0])
	if _, e := choose(s, cfg, 10); faultKind(e) != "duplicate_slot" {
		t.Fatal(e)
	}
}

func TestPeriodsAndCalendar(t *testing.T) {
	for _, tc := range []struct {
		b, e string
		want []string
	}{{"08:00", "10:00", []string{"0"}}, {"12:00", "14:00", []string{"1"}}, {"19:00", "21:00", []string{"2"}}, {"11:00", "13:00", []string{"0", "1"}}} {
		if got := periods(tc.b, tc.e); !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got, tc.want)
		}
	}
	cfg := config(t)
	now := time.Date(2026, 12, 31, 16, 1, 0, 0, time.UTC)
	if got := targetDate(cfg, now); got != "2027-01-03" {
		t.Fatal(got)
	}
	start, end := window(cfg, now)
	if start.Hour() != 12 || end.Sub(start) != 90*time.Second || start.Day() != 1 {
		t.Fatal(start, end)
	}
}

func TestParseStringAndArrayResponses(t *testing.T) {
	s, _ := json.Marshal(pair("YMQ004", "12"))
	for _, raw := range []any{json.RawMessage(s), string(s)} {
		b, _ := json.Marshal(map[string]any{"type": 1, "resultdata": raw})
		got, e := parseSlots(b)
		if e != nil || len(got) != 2 {
			t.Fatal(got, e)
		}
	}
	for _, bad := range []string{"<html>", "{\"type\":1}", "{\"type\":1,\"resultdata\":\"null\"}", "{\"type\":1,\"resultdata\":[{}]}"} {
		if _, e := parseSlots([]byte(bad)); e == nil {
			t.Fatal(bad)
		}
	}
}

func TestDateClampNeverUsed(t *testing.T) {
	cfg := config(t)
	queries := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Field/GetWeek" {
			fmt.Fprint(w, "[]")
			return
		}
		queries++
		fmt.Fprint(w, "{}")
	})
	if _, e := c.slots(context.Background(), cfg); faultKind(e) != "date_not_released" || queries != 0 {
		t.Fatal(e, queries)
	}
	c = fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Field/GetWeek" {
			fmt.Fprint(w, dateJSON(cfg))
			return
		}
		s := pair("YMQ004", "12")
		s[0].DateBeginTime = time.Now().In(shanghai).Format("2006-01-02") + " 19:00:00"
		json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": s})
	})
	if _, e := c.slots(context.Background(), cfg); e != nil {
		t.Fatal("template date must not override GetWeek", e)
	}
}

func TestInventoryOverloadIsDistinctFromSchemaChange(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"Type":0,"Message":"访问人数过多，请稍后刷新重试"}`, "inventory_busy"},
		{`{"Type":"0","Message":"访问人数过多，请稍后刷新重试"}`, "inventory_busy"},
		{`{"Type":0,"Message":"unrecognized"}`, "schema_changed"},
		{`{"Type":0,"ResultData":[]}`, "schema_changed"},
		{`{"Type":1,"Message":"获取成功","ResultData":{}}`, "schema_changed"},
	} {
		_, err := parseSlots([]byte(tc.body))
		if faultKind(err) != tc.want {
			t.Fatalf("got %v, want %s", err, tc.want)
		}
	}
}
