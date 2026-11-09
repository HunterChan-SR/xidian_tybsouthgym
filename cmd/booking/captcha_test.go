package main

import (
	"context"
	"encoding/json"
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
		{`{"type":1,"token":"a","bg":"bad-image","piece":"bad-image"}`, "booking_captcha_protocol", 0},
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
	for _, body := range []string{`{"token":"wrong","x":100}`, `{"token":"ok","x":-1}`, `{"token":"ok","x":251}`, `{"token":"ok","x":300}`, `{"token":"ok","x":100,"cancelled":true}`, `{"token":"ok"}`, `{"token":"ok","x":null}`, `{"token":"ok","x":1.5}`} {
		var reply captchaReply
		if json.Unmarshal([]byte(body), &reply) != nil {
			continue
		}
		if _, err := captchaValues(reply, "ok", 250); err == nil {
			t.Fatal("invalid reply accepted")
		}
	}
	x := 123
	v, err := captchaValues(captchaReply{Token: "ok", X: &x}, "ok", 250)
	if err != nil || v.Get("SliderToken") != "ok" || v.Get("SliderX") != "123" {
		t.Fatal(v, err)
	}
	if rejectionKind("验证码已失效，请刷新页面后重新拖动滑块") != "booking_captcha_rejected" {
		t.Fatal("captcha rejection not classified")
	}
}

func TestCaptchaImageAndReplyBounds(t *testing.T) {
	for _, tc := range []struct {
		bgWidth, bgHeight, pieceWidth, pieceHeight, maxX int
	}{
		{300, 150, 50, 50, 250},
		{50, 50, 50, 50, 0},
		{51, 150, 50, 50, 1},
		{800, 150, 50, 50, 750},
		{49, 150, 50, 50, -1},
		{300, 49, 50, 50, -1},
	} {
		bg, piece := oracleTestImageSizes(t, tc.bgWidth, tc.bgHeight, tc.pieceWidth, tc.pieceHeight)
		maxX, err := captchaMaxX(bg, piece)
		if tc.maxX < 0 {
			if faultKind(err) != "booking_captcha_protocol" {
				t.Fatal("oversize piece accepted", tc, err)
			}
			continue
		}
		if err != nil || maxX != tc.maxX {
			t.Fatal(tc, maxX, err)
		}
		for _, x := range []int{0, maxX} {
			if _, err := captchaValues(captchaReply{Token: "ok", X: &x}, "ok", maxX); err != nil {
				t.Fatal("valid endpoint rejected", tc, x, err)
			}
		}
		x := maxX + 1
		if _, err := captchaValues(captchaReply{Token: "ok", X: &x}, "ok", maxX); err == nil {
			t.Fatal("out-of-bounds reply accepted", tc)
		}
	}
	bg, piece := oracleTestImages(t)
	for _, images := range [][2]string{{"", piece}, {bg, ""}, {"bad!", piece}, {bg, "bm90IGFuIGltYWdl"}} {
		if _, err := captchaMaxX(images[0], images[1]); faultKind(err) != "booking_captcha_protocol" {
			t.Fatal("invalid image accepted", err)
		}
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

func TestOraclePreviewHasNoOrderPath(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint("persistent=", persistent), func(t *testing.T) {
			testOraclePreviewHasNoOrderPath(t, persistent)
		})
	}
}

func testOraclePreviewHasNoOrderPath(t *testing.T, persistent bool) {
	background, piece := oracleTestImages(t)
	cfg := config(t)
	cfg.AllowLive = false
	cfg.BookingCaptcha = true
	cfg.CaptchaSolverCommand = oracleTestCommand(t, "success")
	cfg.CaptchaSolverPersistent = persistent
	if persistent {
		cfg.CaptchaSolverCommand = oracleStdioTestCommand(t, "success")
	}
	requests := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			fmt.Fprint(w, `{"limitcount":10}`)
		case "/Field/GetFieldSliderCaptcha":
			requests++
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "token": "preview", "bg": background, "piece": piece})
		default:
			t.Errorf("preview made an unexpected request to %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	out, err := previewCaptcha(context.Background(), []*Client{c}, cfg)
	if err != nil || out.Status != "captcha_preview_completed" || out.OrderID != "" || requests != 1 {
		t.Fatalf("unexpected oracle preview: %+v, %v, requests=%d", out, err, requests)
	}
}
