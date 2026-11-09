// 会话加载、身份去重和有界只读预检。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

type Account struct {
	Name       string
	CookieFile string
}

type poolMember struct {
	disabled bool
	client   *Client
	limit    int
	next     time.Time
}

func loadSession(path string) (Session, error) {
	var s Session
	b, e := os.ReadFile(path)
	if e != nil {
		return s, errors.New("缺少登录会话，请先运行 python main.py login")
	}
	if json.Unmarshal(b, &s) != nil || s.Cookie == "" || strings.ContainsAny(s.Cookie, "\r\n") {
		return s, errors.New("会话文件无效")
	}
	if s.UserAgent == "" {
		s.UserAgent = "OrderFieldSimulator/2"
	}
	return s, nil
}

func accountKey(s Session) string {
	for _, name := range []string{"UserId", "WXOpenId"} {
		for _, part := range strings.Split(s.Cookie, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 && kv[0] == name && kv[1] != "" {
				h := sha256.Sum256([]byte(name + "=" + kv[1]))
				return hex.EncodeToString(h[:8])
			}
		}
	}
	h := sha256.Sum256([]byte(s.Cookie))
	return hex.EncodeToString(h[:8])
}

func preparePool(ctx context.Context, clients []*Client) ([]poolMember, error) {
	var ready []poolMember
	var transient error
	seen := map[string]bool{}
	for _, client := range clients {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		client.preflightError = nil
		key := accountKey(client.Session)
		if seen[key] {
			continue
		}
		if e := client.checkLogin(ctx); e != nil {
			client.preflightError = e
			client.event(map[string]any{"event": "account_unavailable", "account": client.Name, "reason": faultKind(e)})
			if faultKind(e) == "rate_limited" {
				return nil, e
			}
			if retryablePreflight(e) {
				var prior, current *Fault
				if transient == nil || (errors.As(transient, &prior) && errors.As(e, &current) && current.RetryAfter > prior.RetryAfter) {
					transient = e
				}
			}
			continue
		}
		limit, e := client.limit(ctx, "01")
		if e != nil {
			client.preflightError = e
			client.event(map[string]any{"event": "account_unavailable", "account": client.Name, "reason": faultKind(e)})
			if faultKind(e) == "rate_limited" {
				return nil, e
			}
			if retryablePreflight(e) {
				var prior, current *Fault
				if transient == nil || (errors.As(transient, &prior) && errors.As(e, &current) && current.RetryAfter > prior.RetryAfter) {
					transient = e
				}
			}
			continue
		}
		seen[key] = true
		ready = append(ready, poolMember{client: client, limit: limit})
	}
	if len(ready) == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if transient != nil {
			return nil, transient
		}
		return nil, &Fault{Kind: "no_usable_accounts"}
	}
	return ready, nil
}

func retryablePreflight(err error) bool {
	switch faultKind(err) {
	case "network_error", "server_error", "rate_limited":
		return true
	}
	return false
}

// Only read-only session and venue checks are retried. Order requests never use this helper.
func preparePoolWithRetry(ctx context.Context, clients []*Client, wait func(context.Context, time.Duration) error) ([]poolMember, error) {
	for attempt := 0; ; attempt++ {
		ready, err := preparePool(ctx, clients)
		if err == nil || !retryablePreflight(err) || attempt == 2 {
			return ready, err
		}
		delay := time.Duration(2<<attempt) * time.Second
		var f *Fault
		if errors.As(err, &f) && f.RetryAfter > delay {
			delay = f.RetryAfter
		}
		retryClients := []*Client{}
		for _, client := range clients {
			if retryablePreflight(client.preflightError) {
				retryClients = append(retryClients, client)
			}
		}
		clients = retryClients
		if err = wait(ctx, delay); err != nil {
			return nil, err
		}
	}
}
