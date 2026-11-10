// 平台协议：查询传输、响应边界和脱敏请求记录。
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const origin = "https://tybsouthgym.xidian.edu.cn"

type Client struct {
	preconnect     *preparedConnection
	oracle         *oracleWorker
	preflightError error
	Name           string
	ClockOffsets   []time.Duration
	EventLog       string
	LastHTTPStatus int
	LastServerDate string
	HTTP           *http.Client
	Base           string
	Session        Session
}

func newClient(s Session, timeout time.Duration) *Client {
	jar, _ := cookiejar.New(nil)
	baseURL, _ := url.Parse(origin)
	cookieRequest := &http.Request{Header: http.Header{}}
	cookieRequest.Header.Set("Cookie", s.Cookie)
	jar.SetCookies(baseURL, cookieRequest.Cookies())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Share TLS tickets between this account's reads and fresh mutation connections.
	// No TLS 0-RTT or request replay; normal certificate verification remains enabled.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(8)
	return &Client{Base: origin, Session: s, HTTP: &http.Client{Transport: transport, Timeout: timeout, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) request(ctx context.Context, path string, q url.Values, oneShot bool) ([]byte, error) {
	values := url.Values{}
	for k, v := range q {
		values[k] = v
	}
	values.Set("_", strconv.FormatInt(time.Now().UnixMilli(), 10))
	req, e := http.NewRequestWithContext(ctx, "GET", c.Base+path+"?"+values.Encode(), nil)
	if e != nil {
		return nil, &Fault{Kind: "request_error"}
	}
	req.Header.Set("Cache-Control", "no-cache, no-store")
	req.Header.Set("User-Agent", c.Session.UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", c.Base+"/Views/Field/FieldOrder.html?VenueNo=01&FieldTypeNo=001")
	httpClient := c.HTTP
	if oneShot {
		var closeTransport func()
		httpClient, closeTransport = c.oneShotClient(req)
		defer closeTransport()
	}

	r, e := httpClient.Do(req)
	if e != nil {
		return nil, &Fault{Kind: "network_error"}
	}
	defer r.Body.Close()
	c.LastHTTPStatus = r.StatusCode
	c.LastServerDate = r.Header.Get("Date")
	if r.StatusCode >= 300 && r.StatusCode < 400 || r.StatusCode == 401 || r.StatusCode == 403 {
		return nil, &Fault{Kind: "login_required"}
	}
	if r.StatusCode == 429 {
		delay := 5 * time.Second
		if sec, err := strconv.Atoi(r.Header.Get("Retry-After")); err == nil && sec > 0 {
			delay = time.Duration(sec) * time.Second
		} else if d, err := http.ParseTime(r.Header.Get("Retry-After")); err == nil && time.Until(d) > 0 {
			delay = time.Until(d)
		}
		return nil, &Fault{Kind: "rate_limited", RetryAfter: delay}
	}
	if r.StatusCode >= 500 {
		return nil, &Fault{Kind: "server_error"}
	}
	if r.StatusCode != 200 {
		return nil, &Fault{Kind: "http_error"}
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024+1))
	if e != nil {
		return nil, &Fault{Kind: "network_error"}
	}
	if len(b) > 2*1024*1024 {
		return nil, &Fault{Kind: "response_too_large"}
	}
	b = []byte(strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff")))
	if len(b) == 0 || b[0] == '<' {
		return nil, &Fault{Kind: "unexpected_response"}
	}
	return b, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	oneShot := path == "/Field/OrderField" || path == "/Field/OrderFieldFree" || path == "/Field/FieldCancel"
	return c.getUsing(ctx, path, q, oneShot)
}

func (c *Client) getUsing(ctx context.Context, path string, q url.Values, oneShot bool) ([]byte, error) {
	started := time.Now()
	network := newRequestTiming(started)
	ctx = httptrace.WithClientTrace(ctx, network.trace())
	c.LastHTTPStatus = 0
	c.LastServerDate = ""
	body, err := c.request(ctx, path, q, oneShot)
	if err == nil && (path == "/User/CheckUserStatus" || path == "/Field/GetOrderInfo") {
		if serverTime, e := http.ParseTime(c.LastServerDate); e == nil {
			c.ClockOffsets = append(c.ClockOffsets, serverTime.Sub(started.Add(time.Since(started)/2)))
			if len(c.ClockOffsets) > 3 {
				c.ClockOffsets = c.ClockOffsets[len(c.ClockOffsets)-3:]
			}
		}
	}
	record := map[string]any{"at": started.In(shanghai).Format(time.RFC3339Nano), "path": path, "account": c.Name,
		"httpStatus": c.LastHTTPStatus, "latencyMs": time.Since(started).Milliseconds(), "serverDate": c.LastServerDate}
	record["network"] = network.snapshot()
	if err != nil {
		record["error"] = faultKind(err)
	}
	for _, key := range []string{"dateadd", "TimePeriod", "VenueNo", "FieldTypeNo"} {
		if v := q.Get(key); v != "" {
			record[key] = v
		}
	}
	var env Envelope
	if json.Unmarshal(body, &env) == nil {
		record["type"] = atom(env.Type)
		if env.Message != "" {
			record["message"] = c.redact(env.Message)
		}
	}
	c.event(record)
	return body, err
}

func (c *Client) checkLogin(ctx context.Context) error {
	b, e := c.get(ctx, "/User/CheckUserStatus", nil)
	if e != nil {
		return e
	}
	if string(b) != "1" {
		return &Fault{Kind: "login_required"}
	}
	return nil
}

func (c *Client) limit(ctx context.Context, venue string) (int, error) {
	b, e := c.get(ctx, "/Field/GetOrderInfo", url.Values{"VenueNo": {venue}})
	if e != nil {
		return 0, e
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return 0, &Fault{Kind: "schema_changed"}
	}
	n, e := strconv.Atoi(atom(m["limitcount"]))
	if e != nil || n < 1 {
		return 0, &Fault{Kind: "schema_changed"}
	}
	return n, nil
}
