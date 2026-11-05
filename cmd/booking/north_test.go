package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func northConfig(t *testing.T) Config {
	c := config(t)
	c.FieldTypeNo = "007"
	c.FieldPrefix = "BXQYMQ"
	c.PreferredFields = []string{"BXQYMQ01", "BXQYMQ02", "BXQYMQ03", "BXQYMQ04"}
	c.EveningFlexible = true
	c.MaxTotalCents = 4000
	c.PoolID = "north-test"
	return c
}
func northSlot(field, begin, end, price string) Slot {
	s := slot(field, begin, end, price)
	s.FieldTypeNo = "007"
	return s
}
func TestNorthScopeAndPreference(t *testing.T) {
	c := northConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.MaxTotalCents = 4001 }, func(c *Config) { c.FieldTypeNo = "010" }, func(c *Config) { c.PreferredFields = []string{"BXQYMQ05"} }, func(c *Config) { c.DateAdd = 1 }, func(c *Config) { c.EveningFlexible = false }} {
		bad := c
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("scope accepted", bad)
		}
	}
	slots := []Slot{northSlot("BXQYMQ01", "19:00", "20:00", "20"), northSlot("BXQYMQ02", "20:00", "21:00", "20"), northSlot("BXQYMQ02", "21:00", "22:00", "20")}
	got, err := choose(slots, c, 10)
	if err != nil || len(got) != 1 || got[0].Items[0].BeginTime != "20:00" || len(got[0].Items) != 2 {
		t.Fatal(got, err)
	}
	slots = append(slots, northSlot("BXQYMQ01", "20:00", "21:00", "20"))
	got, err = choose(slots, c, 10)
	if err != nil || got[0].Items[0].BeginTime != "19:00" || got[0].Items[0].FieldNo != "BXQYMQ01" {
		t.Fatal(got, err)
	}
	c.MaxTotalCents = 2400
	got, err = choose(slots, c, 10)
	if err != nil || len(got[0].Items) != 1 || got[0].TotalCents != 2000 {
		t.Fatal(got, err)
	}
	c.MaxTotalCents = 1900
	got, err = choose(slots, c, 10)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	c.MaxTotalCents = 4000
	got, err = choose([]Slot{northSlot("BXQYMQ05", "19:00", "20:00", "20"), northSlot("BXQYMQ05", "20:00", "21:00", "20")}, c, 10)
	if err != nil || len(got) != 0 {
		t.Fatal("unauthorized field", got, err)
	}
	dup := append(slots, slots[0])
	if _, err = choose(dup, c, 10); faultKind(err) != "duplicate_slot" {
		t.Fatal(err)
	}
	slots[0].FinalPrice = nil
	if _, err = choose(slots, c, 10); faultKind(err) != "price_missing_or_invalid" {
		t.Fatal(err)
	}
}
func TestNorthSelectedWindowVerificationAndGlobalGuard(t *testing.T) {
	for _, tc := range []struct {
		name            string
		single, unknown bool
		daytime         bool
	}{{"two-hour", false, false, false}, {"single-hour", true, false, false}, {"unknown-stop", false, true, false}, {"daytime-two", false, false, true}, {"daytime-one", true, false, true}, {"daytime-unknown", false, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := northConfig(t)
			cfg.ReleaseTime = time.Now().In(shanghai).Add(2 * time.Second).Format("15:04:05")
			slots := []Slot{northSlot("BXQYMQ02", "20:00", "21:00", "20")}
			if !tc.single {
				slots = append(slots, northSlot("BXQYMQ02", "21:00", "22:00", "20"))
			}
			if tc.daytime {
				cfg.AllDayFallback = true
				for i := range slots {
					if slots[i].BeginTime == "20:00" {
						slots[i].BeginTime = "11:00"
						slots[i].EndTime = "12:00"
					} else {
						slots[i].BeginTime = "12:00"
						slots[i].EndTime = "13:00"
					}
				}
			}
			expectedBegin := slots[0].BeginTime
			submissions := 0
			c := fake(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/User/CheckUserStatus":
					fmt.Fprint(w, "1")
				case "/Field/GetOrderInfo":
					fmt.Fprint(w, `{"limitcount":10}`)
				case "/Field/GetWeek":
					fmt.Fprint(w, dateJSON(cfg))
				case "/Field/GetVenueStateNew":
					returned := slots
					if tc.daytime {
						returned = []Slot{}
						for _, s := range slots {
							ps := periods(s.BeginTime, s.EndTime)
							if ps[0] == r.URL.Query().Get("TimePeriod") {
								returned = append(returned, s)
							}
						}
					}
					json.NewEncoder(w).Encode(map[string]any{"type": 1, "resultdata": returned})
				case "/Field/OrderField":
					submissions++
					var items []Item
					if json.Unmarshal([]byte(r.URL.Query().Get("checkdata")), &items) != nil || items[0].BeginTime != expectedBegin {
						t.Error("wrong selected request")
					}
					if tc.unknown {
						fmt.Fprint(w, `{}`)
					} else {
						fmt.Fprint(w, `{"type":1,"resultdata":"north-oid"}`)
					}
				case "/Field/GetPayInfo":
					rows := []map[string]any{}
					for _, s := range slots {
						rows = append(rows, map[string]any{"FieldNo": s.FieldNo, "OrderDate": targetDate(cfg, time.Now()), "BeginTime": s.BeginTime, "EndTime": s.EndTime, "Price": "20.00"})
					}
					json.NewEncoder(w).Encode(rows)
				default:
					t.Error(r.URL.Path)
				}
			})
			c.Name = "account-test"
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			out, err := runPool(ctx, []*Client{c}, cfg, "book")
			if tc.unknown {
				if faultKind(err) != "order_uncertain" {
					t.Fatal(out, err)
				}
			} else if err != nil || out.BeginTime != expectedBegin || out.TotalCents != len(slots)*2000 {
				t.Fatal(out, err)
			}
			if submissions != 1 {
				t.Fatal(submissions)
			}
			other := cfg
			other.BeginTime = "18:00"
			other.EndTime = "20:00"
			c.Session.Cookie = "UserId=second-account"
			cand, _ := choose(slots, cfg, 10)
			_, err = guardedSubmit(ctx, c, other, cand[0], targetDate(cfg, time.Now()))
			if faultKind(err) != "existing_order_or_uncertain_attempt" || submissions != 1 {
				t.Fatal("global receipt broken", err, submissions)
			}
		})
	}
}

func TestAllDayFallbackPriorityAndScope(t *testing.T) {
	cfg := northConfig(t)
	cfg.AllDayFallback = true
	slots := []Slot{northSlot("BXQYMQ01", "10:00", "11:00", "20"), northSlot("BXQYMQ01", "11:00", "12:00", "20"), northSlot("BXQYMQ02", "16:00", "17:00", "20")}
	got, err := choose(slots, cfg, 10)
	if err != nil || len(got) == 0 || got[0].Items[0].BeginTime != "10:00" || len(got[0].Items) != 2 {
		t.Fatal(got, err)
	}
	slots = append(slots, northSlot("BXQYMQ03", "19:00", "20:00", "20"))
	got, err = choose(slots, cfg, 10)
	if err != nil || got[0].Items[0].BeginTime != "19:00" {
		t.Fatal("evening priority", got, err)
	}
	cfg.AllDayFallback = false
	got, err = choose(slots[:3], cfg, 10)
	if err != nil || len(got) != 0 {
		t.Fatal("disabled fallback", got, err)
	}
	cfg.AllDayFallback = true
	cfg.MaxTotalCents = 1900
	got, err = choose(slots, cfg, 10)
	if err != nil || len(got) != 0 {
		t.Fatal("budget", got, err)
	}
	cfg.MaxTotalCents = 4000
	got, err = choose([]Slot{northSlot("BXQYMQ05", "10:00", "11:00", "20")}, cfg, 10)
	if err != nil || len(got) != 0 {
		t.Fatal("court scope", got, err)
	}
	bad := config(t)
	bad.AllDayFallback = true
	if bad.Validate() == nil {
		t.Fatal("south fallback accepted")
	}
}
