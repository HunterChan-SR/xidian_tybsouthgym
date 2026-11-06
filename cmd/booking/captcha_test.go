package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestBookingCaptchaGatesMutation(t *testing.T) {
	for _, tc := range []struct {
		body, kind string
		want       int
	}{
		{`{"type":0}`, "", 1},
		{`{"type":2,"retryafter":15}`, "rate_limited", 0},
		{`{"type":1,"token":"a"}`, "booking_captcha_protocol", 0},
		{`{}`, "booking_captcha_protocol", 0},
		{`broken`, "booking_captcha_protocol", 0},
	} {
		t.Run(tc.body, func(t *testing.T) {
			cfg := config(t)
			cfg.BookingCaptcha = true
			calls := 0
			c := fake(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Field/GetFieldSliderCaptcha" {
					fmt.Fprint(w, tc.body)
					return
				}
				calls++
				fmt.Fprint(w, `{"type":1,"resultdata":"test-order"}`)
			})
			cand, _ := choose(pair("YMQ004", "12"), cfg, 10)
			_, err := guardedSubmit(context.Background(), c, cfg, cand[0], "2026-11-08")
			kind := ""
			if err != nil {
				kind = faultKind(err)
			}
			if kind != tc.kind || calls != tc.want {
				t.Fatal(err, calls)
			}
			if tc.want == 0 {
				if _, err := os.Stat(receiptPath(cfg, c.Session, "2026-11-08")); !os.IsNotExist(err) {
					t.Fatal("captcha failure created order protection", err)
				}
			}
		})
	}
}
func TestHumanCaptchaProof(t *testing.T) {
	for _, r := range []captchaReply{{Token: "wrong", X: 100}, {Token: "ok", X: -1}, {Token: "ok", X: 301}, {Token: "ok", X: 100, Cancelled: true}} {
		if _, err := captchaValues(r, "ok"); err == nil {
			t.Fatal("invalid reply accepted")
		}
	}
	v, err := captchaValues(captchaReply{Token: "ok", X: 123}, "ok")
	if err != nil || v.Get("SliderToken") != "ok" || v.Get("SliderX") != "123" {
		t.Fatal(v, err)
	}
	if rejectionKind("验证码已失效，请刷新页面后重新拖动滑块") != "booking_captcha_rejected" {
		t.Fatal("captcha rejection not classified")
	}
}
func TestWeekendTargetBothDays(t *testing.T) {
	cfg := config(t)
	cfg.WeekendAfternoon = true
	for _, tc := range []struct {
		date string
		want bool
	}{{"2026-11-05", true}, {"2026-11-06", true}, {"2026-11-07", false}} {
		now, _ := time.ParseInLocation("2006-01-02", tc.date, shanghai)
		if weekendAfternoon(cfg, now) != tc.want {
			t.Fatal(tc)
		}
	}
}

func TestCaptchaPreviewHasNoOrderPath(t *testing.T) {
	cfg := config(t)
	cfg.AllowLive = false
	var challengeRequests int
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			fmt.Fprint(w, `{"limitcount":10}`)
		case "/Field/GetFieldSliderCaptcha":
			challengeRequests++
			fmt.Fprint(w, `{"type":0}`)
		default:
			t.Errorf("preview made an unexpected request to %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	out, err := previewCaptcha(context.Background(), []*Client{c}, cfg)
	if err != nil || out.Status != "captcha_preview_not_required" || out.OrderID != "" || challengeRequests != 1 {
		t.Fatalf("unexpected preview outcome: %+v, %v, requests=%d", out, err, challengeRequests)
	}
}
