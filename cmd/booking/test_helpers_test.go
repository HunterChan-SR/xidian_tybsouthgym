package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func config(t *testing.T) Config {
	t.Helper()
	return Config{VenueNo: "01", FieldTypeNo: "001", DateAdd: 2, BeginTime: "19:00", EndTime: "21:00", PreferredFields: []string{"YMQ004"}, AllowOtherFields: true, FieldPrefix: "YMQ", MaxTotalCents: 2400, ReleaseTime: "12:00:00", WindowSeconds: 90, PollSeconds: 2, RequestTimeoutSeconds: 2, CookieFile: "session.json", StateDir: t.TempDir(), AllowLive: true}
}

func slot(field, begin, end, price string) Slot {
	return Slot{FieldNo: field, FieldName: field, FieldTypeNo: "001", BeginTime: begin, EndTime: end, FinalPrice: json.RawMessage(fmt.Sprintf("%q", price)), FieldState: json.RawMessage("0"), TimeStatus: json.RawMessage("\"1\"")}
}

func pair(field, price string) []Slot {
	return []Slot{slot(field, "19:00", "20:00", price), slot(field, "20:00", "21:00", price)}
}

func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	c := newClient(Session{Cookie: "UserId=test; session=secret"}, time.Second)
	c.Base = server.URL
	return c
}

func dateJSON(cfg Config) string {
	var dates []map[string]string
	for i := 0; i <= cfg.DateAdd; i++ {
		dates = append(dates, map[string]string{"Date": time.Now().In(shanghai).AddDate(0, 0, i).Format("01-02")})
	}
	b, _ := json.Marshal(dates)
	return string(b)
}
