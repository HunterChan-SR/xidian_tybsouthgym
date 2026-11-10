// 提前完成 TLS 握手；连接尚未承载 HTTP，只交给一次下单请求。
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"time"
)

const orderPreconnectLead = 750 * time.Millisecond
const orderPreconnectMaxAge = 5 * time.Second

type preparedConnection struct {
	mu      sync.Mutex
	conn    net.Conn
	target  string
	readyAt time.Time
	closed  bool
	done    chan struct{}
	cancel  context.CancelFunc
	expiry  *time.Timer
}

// close also covers a handshake still in progress. A consumed connection belongs
// to its one-shot Transport, and is no longer closed by this holder.
func (p *preparedConnection) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cancel()
	if p.expiry != nil {
		p.expiry.Stop()
	}
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
}

func (p *preparedConnection) take(target string) (net.Conn, time.Duration) {
	if p == nil {
		return nil, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.conn == nil {
		return nil, 0
	}
	age := time.Since(p.readyAt)
	if target != p.target || age > orderPreconnectMaxAge {
		p.conn.Close()
		p.conn = nil
		return nil, 0
	}
	conn := p.conn
	p.conn = nil
	if p.expiry != nil {
		p.expiry.Stop()
	}
	return conn, age
}

// No gym HTTP request is sent here. HTTP proxies use their configured CONNECT
// route; unsupported proxy/custom TLS dial paths retain the existing Transport.
func (c *Client) startOrderPreconnect(parent context.Context) *preparedConnection {
	c.closeOrderPreconnect()
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	p := &preparedConnection{done: make(chan struct{}), cancel: cancel}
	c.preconnect = p
	go func() {
		defer close(p.done)
		defer cancel()
		started := time.Now()
		status := "failed"
		timing := newRequestTiming(started)
		ctx := httptrace.WithClientTrace(ctx, timing.trace())
		conn, target, err := dialUnusedTLS(ctx, c.HTTP, c.Base)
		if err == nil {
			p.mu.Lock()
			if p.closed || ctx.Err() != nil {
				conn.Close()
			} else {
				p.conn, p.target, p.readyAt = conn, target, time.Now()
				p.expiry = time.AfterFunc(orderPreconnectMaxAge, p.close)
				status = "ready"
			}
			p.mu.Unlock()
		} else {
			status = faultKind(err)
		}
		c.event(map[string]any{"event": "order_tls_preconnect", "account": c.Name,
			"status": status, "elapsedMs": elapsedMS(started), "network": timing.snapshot()})
	}()
	return p
}

func (c *Client) closeOrderPreconnect() {
	if c.preconnect != nil {
		c.preconnect.close()
		<-c.preconnect.done
		c.preconnect = nil
	}
}

func dialUnusedTLS(ctx context.Context, client *http.Client, base string) (net.Conn, string, error) {
	u, err := url.Parse(base)
	tr, ok := client.Transport.(*http.Transport)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || !ok || tr.DialTLSContext != nil || tr.DialTLS != nil {
		return nil, "", &Fault{Kind: "preconnect_unsupported"}
	}
	var proxy *url.URL
	if tr.Proxy != nil {
		proxy, err = tr.Proxy(&http.Request{URL: u})
		if err != nil {
			return nil, "", &Fault{Kind: "preconnect_proxy_error"}
		}
	}
	conf := &tls.Config{}
	if tr.TLSClientConfig != nil {
		conf = tr.TLSClientConfig.Clone()
	}
	if conf.ServerName == "" {
		conf.ServerName = u.Hostname()
	}
	conf.NextProtos = []string{"http/1.1"}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	address := net.JoinHostPort(u.Hostname(), port)
	raw, err := dialPreconnectTCP(ctx, tr, proxy, address)
	if err != nil {
		return nil, "", err
	}
	conn := tls.Client(raw, conf)
	trace := httptrace.ContextClientTrace(ctx)
	if trace != nil && trace.TLSHandshakeStart != nil {
		trace.TLSHandshakeStart()
	}
	err = conn.HandshakeContext(ctx)
	if trace != nil && trace.TLSHandshakeDone != nil {
		trace.TLSHandshakeDone(conn.ConnectionState(), err)
	}
	if err != nil {
		conn.Close()
		return nil, "", &Fault{Kind: "preconnect_tls_error"}
	}
	return conn, u.Host, nil
}

// The Transport sees a fresh connection (no prior HTTP exchange), so its reused
// connection retry path cannot replay the GET-shaped mutation. HTTP/2 is disabled.
func (c *Client) oneShotClient(req *http.Request) (*http.Client, func()) {
	base, ok := c.HTTP.Transport.(*http.Transport)
	if !ok {
		base = http.DefaultTransport.(*http.Transport)
	}
	tr := base.Clone()
	tr.DisableKeepAlives = true
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if tr.TLSClientConfig != nil {
		tr.TLSClientConfig.NextProtos = []string{"http/1.1"}
	}
	conn, age := c.preconnect.take(req.URL.Host)
	if conn != nil {
		// This socket already includes the configured proxy tunnel, if any.
		tr.Proxy = nil
		var once sync.Once
		tr.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			var selected net.Conn
			once.Do(func() { selected = conn })
			if selected == nil {
				return nil, &Fault{Kind: "preconnect_already_used"}
			}
			return selected, nil
		}
		c.event(map[string]any{"event": "order_tls_preconnect_consumed", "account": c.Name, "ageMs": float64(age.Microseconds()) / 1000})
	}
	copyClient := *c.HTTP
	copyClient.Transport = tr
	return &copyClient, func() {
		tr.CloseIdleConnections()
		if conn != nil {
			conn.Close()
		}
	}
}

// Exercise the same one-shot TLS handoff with a read-only login-status request.
// No candidate, challenge, OrderField or cancellation request is involved.
func previewOrderTLS(ctx context.Context, clients []*Client) (Outcome, error) {
	out := Outcome{Status: "tls_preview_started"}
	ready, err := preparePool(ctx, clients)
	if err != nil {
		return out, err
	}
	c := ready[0].client
	out.Account = c.Name
	p := c.startOrderPreconnect(ctx)
	defer c.closeOrderPreconnect()
	select {
	case <-ctx.Done():
		return out, ctx.Err()
	case <-p.done:
	}
	p.mu.Lock()
	usable := p.conn != nil
	p.mu.Unlock()
	if !usable {
		return out, &Fault{Kind: "tls_preview_not_ready"}
	}
	if err := sleepContext(ctx, orderPreconnectLead); err != nil {
		return out, err
	}
	body, err := c.getUsing(ctx, "/User/CheckUserStatus", nil, true)
	if err != nil {
		return out, err
	}
	if string(body) != "1" {
		return out, &Fault{Kind: "login_required"}
	}
	out.Status = "tls_preview_completed"
	return out, nil
}

// Open the same HTTP CONNECT tunnel the configured Transport would use.
// This is a proxy connection request, not an HTTP request to the gym platform.
func dialPreconnectTCP(ctx context.Context, tr *http.Transport, proxy *url.URL, target string) (net.Conn, error) {
	address := target
	if proxy != nil {
		if proxy.Scheme != "http" || proxy.Hostname() == "" {
			return nil, &Fault{Kind: "preconnect_proxy_unsupported"}
		}
		port := proxy.Port()
		if port == "" {
			port = "80"
		}
		address = net.JoinHostPort(proxy.Hostname(), port)
	}
	dial := tr.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	raw, err := dial(ctx, "tcp", address)
	if err != nil {
		return nil, &Fault{Kind: "preconnect_network_error"}
	}
	if proxy == nil {
		return raw, nil
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		raw.SetDeadline(deadline)
	}
	header := tr.ProxyConnectHeader.Clone()
	if tr.GetProxyConnectHeader != nil {
		header, err = tr.GetProxyConnectHeader(ctx, proxy, target)
		if err != nil {
			raw.Close()
			return nil, &Fault{Kind: "preconnect_proxy_error"}
		}
		header = header.Clone()
	}
	if header == nil {
		header = make(http.Header)
	}
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxy.User.Username()+":"+password)))
	}
	req := (&http.Request{Method: "CONNECT", URL: &url.URL{Opaque: target}, Host: target, Header: header}).WithContext(ctx)
	if err = req.Write(raw); err != nil {
		raw.Close()
		return nil, &Fault{Kind: "preconnect_proxy_error"}
	}
	reader := bufio.NewReader(raw)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		raw.Close()
		return nil, &Fault{Kind: "preconnect_proxy_error"}
	}
	if tr.OnProxyConnectResponse != nil {
		if err = tr.OnProxyConnectResponse(ctx, proxy, req, response); err != nil {
			raw.Close()
			return nil, &Fault{Kind: "preconnect_proxy_error"}
		}
	}
	if response.StatusCode != http.StatusOK {
		raw.Close()
		return nil, &Fault{Kind: "preconnect_proxy_rejected"}
	}
	raw.SetDeadline(time.Time{})
	// A conforming CONNECT peer sends no TLS bytes before ClientHello, but
	// preserve buffered bytes if present instead of silently dropping them.
	if reader.Buffered() > 0 {
		return &bufferedTunnel{Conn: raw, reader: reader}, nil
	}
	return raw, nil
}

type bufferedTunnel struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedTunnel) Read(b []byte) (int, error) { return c.reader.Read(b) }
