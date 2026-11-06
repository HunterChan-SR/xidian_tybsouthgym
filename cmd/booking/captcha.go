// 预约滑块由用户在官方组件中完成；凭据只用于紧接的一次提交。
package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type captchaReply struct {
	Token     string `json:"token"`
	X         int    `json:"x"`
	Cancelled bool   `json:"cancelled"`
}

func (c *Client) bookingCaptcha(ctx context.Context, cfg Config) (url.Values, error) {
	b, err := c.get(ctx, "/Field/GetFieldSliderCaptcha", nil)
	if err != nil {
		return nil, err
	}
	var challenge struct {
		Type  *int   `json:"type"`
		Token string `json:"token"`
		BG    string `json:"bg"`
		Piece string `json:"piece"`
		Retry int    `json:"retryafter"`
	}
	if json.Unmarshal(b, &challenge) != nil || challenge.Type == nil {
		return nil, &Fault{Kind: "booking_captcha_protocol"}
	}
	if *challenge.Type == 0 {
		return url.Values{}, nil
	}
	if *challenge.Type == 2 {
		delay := challenge.Retry
		if delay < 1 {
			delay = 5
		}
		return nil, &Fault{Kind: "rate_limited", RetryAfter: time.Duration(delay) * time.Second}
	}
	if *challenge.Type != 1 || challenge.Token == "" || challenge.BG == "" || challenge.Piece == "" {
		return nil, &Fault{Kind: "booking_captcha_protocol"}
	}
	return c.humanCaptcha(ctx, cfg, b, challenge.Token)
}

func (c *Client) humanCaptcha(ctx context.Context, cfg Config, challenge []byte, token string) (url.Values, error) {
	dir := filepath.Join(cfg.StateDir, "captcha")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "challenge-*.json")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	defer os.Remove(path + ".reply")
	if err = saveJSON(path, map[string]any{"challenge": json.RawMessage(challenge), "account": c.Name}); err != nil {
		return nil, err
	}
	c.event(map[string]any{"event": "booking_captcha_wait", "account": c.Name})
	executable, err := os.Executable()
	root := filepath.Dir(executable)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "python", "-X", "utf8", filepath.Join(root, "main.py"), "captcha", "--request", path)
	cmd.Dir = root
	// Browser is interactive; never print request data or subprocess exceptions containing it.
	cmd.Stdout = os.Stdout
	if err = cmd.Run(); err != nil {
		return nil, &Fault{Kind: "booking_captcha_not_completed"}
	}
	var reply captchaReply
	b, err := os.ReadFile(path + ".reply")
	if err != nil || json.Unmarshal(b, &reply) != nil || reply.Cancelled {
		return nil, &Fault{Kind: "booking_captcha_not_completed"}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return captchaValues(reply, token)
}

func captchaValues(reply captchaReply, token string) (url.Values, error) {
	if reply.Cancelled {
		return nil, &Fault{Kind: "booking_captcha_not_completed"}
	}
	if reply.Token != token || reply.X < 0 || reply.X > 300 {
		return nil, &Fault{Kind: "booking_captcha_protocol"}
	}
	return url.Values{"SliderToken": {reply.Token}, "SliderX": {strconv.Itoa(reply.X)}}, nil

}

// previewCaptcha exercises the production slider path without selecting or submitting any order.
func previewCaptcha(ctx context.Context, clients []*Client, cfg Config) (Outcome, error) {
	out := Outcome{Status: "captcha_preview_started"}
	ready, err := preparePool(ctx, clients)
	if err != nil {
		return out, err
	}
	c := ready[0].client
	out.Account = c.Name
	proof, err := c.bookingCaptcha(ctx, cfg)
	if err != nil {
		return out, err
	}
	if len(proof) == 0 {
		out.Status = "captcha_preview_not_required"
	} else {
		out.Status = "captcha_preview_completed"
	}
	return out, nil
}
