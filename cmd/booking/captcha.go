// 预约滑块由人工窗口或本机求解接口完成；答案只用于当前挑战的下一次提交。
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type captchaReply struct {
	Token     string `json:"token"`
	X         *int   `json:"x"`
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
	maxX, err := captchaMaxX(challenge.BG, challenge.Piece)
	if err != nil {
		return nil, err
	}
	return c.solveCaptcha(ctx, cfg, b, challenge.Token, maxX)
}

// captchaMaxX derives the inclusive original-pixel bound from the challenge,
// independently of the worker's reply. Transparent piece borders count as width.
func captchaMaxX(background, piece string) (int, error) {
	var dimensions [2]image.Config
	for i, encoded := range []string{background, piece} {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return 0, &Fault{Kind: "booking_captcha_protocol"}
		}
		dimensions[i], _, err = image.DecodeConfig(bytes.NewReader(data))
		if err != nil || dimensions[i].Width <= 0 || dimensions[i].Height <= 0 {
			return 0, &Fault{Kind: "booking_captcha_protocol"}
		}
	}
	bg, part := dimensions[0], dimensions[1]
	if part.Width > bg.Width || part.Height > bg.Height {
		return 0, &Fault{Kind: "booking_captcha_protocol"}
	}
	return bg.Width - part.Width, nil
}

func (c *Client) solveCaptcha(ctx context.Context, cfg Config, challenge []byte, token string, maxX int) (url.Values, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	request := map[string]any{"challenge": json.RawMessage(challenge), "account": c.Name, "deadline": deadline.Format(time.RFC3339Nano)}
	if cfg.CaptchaSolverPersistent {
		if c.oracle == nil {
			return nil, &Fault{Kind: "booking_captcha_not_completed"}
		}
		c.event(map[string]any{"event": "booking_captcha_wait", "account": c.Name, "transport": "stdio"})
		started := time.Now()
		var reply captchaReply
		if err := c.oracle.exchange(ctx, request, &reply); err != nil {
			return nil, err
		}
		values, err := captchaValues(reply, token, maxX)
		if err == nil {
			c.event(map[string]any{"event": "booking_captcha_ready", "account": c.Name, "elapsedMs": time.Since(started).Milliseconds(), "transport": "stdio"})
		}
		return values, err
	}
	dir, err := filepath.Abs(filepath.Join(cfg.StateDir, "captcha"))
	if err != nil {
		return nil, err
	}
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
	if err = saveJSON(path, request); err != nil {
		return nil, err
	}
	c.event(map[string]any{"event": "booking_captcha_wait", "account": c.Name})
	executable, err := os.Executable()
	root := filepath.Dir(executable)
	if err != nil {
		return nil, err
	}
	command := append([]string(nil), cfg.CaptchaSolverCommand...)
	if len(command) == 0 {
		command = []string{"python", "-X", "utf8", filepath.Join(root, "main.py"), "captcha"}
	}
	command = append(command, "--request", path)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = root
	// Only the built-in worker has a known redacted output format.
	if len(cfg.CaptchaSolverCommand) == 0 {
		cmd.Stdout = os.Stdout
	}
	started := time.Now()
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
	values, err := captchaValues(reply, token, maxX)
	if err == nil {
		c.event(map[string]any{"event": "booking_captcha_ready", "account": c.Name, "elapsedMs": time.Since(started).Milliseconds()})
	}
	return values, err
}

func captchaValues(reply captchaReply, token string, maxX int) (url.Values, error) {
	if reply.Cancelled {
		return nil, &Fault{Kind: "booking_captcha_not_completed"}
	}
	if reply.Token != token || reply.X == nil || *reply.X < 0 || *reply.X > maxX {
		return nil, &Fault{Kind: "booking_captcha_protocol"}
	}
	return url.Values{"SliderToken": {reply.Token}, "SliderX": {strconv.Itoa(*reply.X)}}, nil

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
	closeWorker, err := attachOracleWorker(ctx, clients, cfg)
	if err != nil {
		return out, err
	}
	defer closeWorker()
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
