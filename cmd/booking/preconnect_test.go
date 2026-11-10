package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func localTLSClient(t *testing.T, server *httptest.Server, name string) *Client {
	t.Helper()
	c := newClient(Session{Cookie: "UserId=" + name}, time.Second)
	c.Name, c.Base = name, server.URL
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	c.HTTP.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	u, _ := url.Parse(server.URL)
	c.HTTP.Jar.SetCookies(u, []*http.Cookie{{Name: "UserId", Value: name}})
	t.Cleanup(func() { c.closeOrderPreconnect(); c.HTTP.CloseIdleConnections() })
	return c
}

func awaitPreconnect(t *testing.T, c *Client) {
	t.Helper()
	p := c.startOrderPreconnect(context.Background())
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		t.Fatal("preconnect did not become ready")
	}
}

func TestPreconnectFreshOnceAndAccountIsolation(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var calls atomic.Int32
			var connections atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				cookie, err := r.Cookie("UserId")
				if err != nil || cookie.Value != r.URL.Query().Get("expected") {
					t.Error("account mismatch")
				}
				if r.ProtoMajor != 1 || !r.Close {
					t.Error("not one-shot HTTP/1.1")
				}
				fmt.Fprint(w, `{"type":1}`)
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.EnableHTTP2 = true
			server.TLS = &tls.Config{MinVersion: version, MaxVersion: version}
			server.StartTLS()
			defer server.Close()
			clients := []*Client{}
			for _, name := range []string{"one", "two", "three"} {
				c := localTLSClient(t, server, name)
				c.EventLog = filepath.Join(t.TempDir(), "events.jsonl")
				c.startOrderPreconnect(context.Background())
				clients = append(clients, c)
			}
			for _, c := range clients {
				<-c.preconnect.done
			}
			if calls.Load() != 0 {
				t.Fatal("prewarming sent HTTP")
			}
			for _, c := range clients {
				if _, err := c.get(context.Background(), "/Field/OrderField", url.Values{"expected": {c.Name}}); err != nil {
					t.Fatal(err)
				}
				data, _ := os.ReadFile(c.EventLog)
				if !strings.Contains(string(data), "order_tls_preconnect_consumed") {
					t.Fatal("warm connection unused")
				}
				var row struct {
					Network map[string]any `json:"network"`
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				json.Unmarshal([]byte(lines[len(lines)-1]), &row)
				if row.Network["connectionReused"] != false || row.Network["writeAttempts"] != float64(1) {
					t.Fatal(row.Network)
				}
				if _, exists := row.Network["connectMs"]; exists {
					t.Fatal("a new TCP connection was made")
				}
				if conn, _ := c.preconnect.take(strings.TrimPrefix(server.URL, "https://")); conn != nil {
					t.Fatal("connection used twice")
				}
			}
			if calls.Load() != 3 || connections.Load() != 3 {
				t.Fatal(calls.Load(), connections.Load())
			}
		})
	}
}

func TestPreconnectLostResponseKeepsGlobalGuard(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer server.Close()
	first, second := localTLSClient(t, server, "one"), localTLSClient(t, server, "two")
	awaitPreconnect(t, first)
	awaitPreconnect(t, second)
	cfg := config(t)
	cfg.PoolID = "one-shared-order"
	candidates, _ := choose(pair("YMQ004", "12"), cfg, 10)
	_, err := guardedSubmit(context.Background(), first, cfg, candidates[0], "2026-11-10")
	if faultKind(err) != "order_uncertain" {
		t.Fatal(err)
	}
	_, err = guardedSubmit(context.Background(), second, cfg, candidates[0], "2026-11-10")
	if faultKind(err) != "existing_order_or_uncertain_attempt" || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
}

func TestPreconnectStaleSocketIsNotRedialed(t *testing.T) {
	for _, endpoint := range []string{"/Field/OrderField", "/Field/OrderFieldFree", "/Field/FieldCancel"} {
		t.Run(endpoint, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer server.Close()
			c := localTLSClient(t, server, "one")
			tr := c.HTTP.Transport.(*http.Transport)
			dial := tr.DialContext
			var dials atomic.Int32
			tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				dials.Add(1)
				return dial(ctx, network, address)
			}
			awaitPreconnect(t, c)
			// Simulate the peer disappearing after the handshake, before HTTP.
			c.preconnect.mu.Lock()
			c.preconnect.conn.Close()
			c.preconnect.mu.Unlock()
			if _, err := c.get(context.Background(), endpoint, nil); faultKind(err) != "network_error" {
				t.Fatal(err)
			}
			if dials.Load() != 1 || calls.Load() != 0 {
				t.Fatal("mutation replayed", dials.Load(), calls.Load())
			}
		})
	}
}

func TestPreconnectExpiryMismatchAndCleanup(t *testing.T) {
	for _, action := range []string{"expired", "wrong-host", "close"} {
		t.Run(action, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") }))
			defer server.Close()
			c := localTLSClient(t, server, "one")
			awaitPreconnect(t, c)
			p := c.preconnect
			target := strings.TrimPrefix(server.URL, "https://")
			if action == "expired" {
				p.mu.Lock()
				p.readyAt = time.Now().Add(-orderPreconnectMaxAge - time.Second)
				p.mu.Unlock()
			}
			if action == "wrong-host" {
				target = "different.example:443"
			}
			if action == "close" {
				c.closeOrderPreconnect()
			}
			if conn, _ := p.take(target); conn != nil {
				conn.Close()
				t.Fatal("invalid connection selected")
			}
		})
	}
}

func TestPreconnectHonorsProxyAndCancelledContext(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") }))
	defer server.Close()
	c := localTLSClient(t, server, "one")
	proxy, _ := url.Parse("socks5://127.0.0.1:1")
	c.HTTP.Transport.(*http.Transport).Proxy = http.ProxyURL(proxy)
	p := c.startOrderPreconnect(context.Background())
	<-p.done
	if p.conn != nil {
		t.Fatal("bypassed configured proxy")
	}
	c.HTTP.Transport.(*http.Transport).Proxy = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = c.startOrderPreconnect(ctx)
	<-p.done
	if p.conn != nil {
		t.Fatal("retained cancelled connection")
	}
}

func TestTLSPreviewSendsOnlyReadRequests(t *testing.T) {
	var statusCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/User/CheckUserStatus":
			statusCalls.Add(1)
			fmt.Fprint(w, "1")
		case "/Field/GetOrderInfo":
			fmt.Fprint(w, `{"limitcount":10}`)
		default:
			t.Error("preview sent unexpected endpoint", r.URL.Path)
		}
	}))
	defer server.Close()
	c := localTLSClient(t, server, "one")
	out, err := previewOrderTLS(context.Background(), []*Client{c})
	if err != nil || out.Status != "tls_preview_completed" || statusCalls.Load() != 2 || c.preconnect != nil {
		t.Fatal(out, err, statusCalls.Load())
	}
}

func TestPreconnectRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") }))
	defer server.Close()
	c := newClient(Session{}, time.Second)
	c.Base = server.URL
	defer c.closeOrderPreconnect()
	p := c.startOrderPreconnect(context.Background())
	<-p.done
	if p.conn != nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestPreconnectHandshakeInProgressIsClosed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			accepted <- conn
		}
	}()
	c := newClient(Session{}, time.Second)
	c.Base = "https://" + listener.Addr().String()
	p := c.startOrderPreconnect(context.Background())
	conn := <-accepted
	defer conn.Close()
	c.closeOrderPreconnect()
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("handshake leaked")
	}
	if c.preconnect != nil {
		t.Fatal("holder retained")
	}
}

// Local proxy forwards only a CONNECT tunnel; it never sees a platform Cookie.
func TestPreconnectUsesConfiguredHTTPProxy(t *testing.T) {
	var platformCalls, proxyCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platformCalls.Add(1)
		if r.ProtoMajor != 1 {
			t.Error("HTTP/2 used")
		}
		fmt.Fprint(w, "1")
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		if r.Method != "CONNECT" || r.Host != target.Host || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" || r.Header.Get("X-Test") != "connect" {
			t.Error("proxy route/auth headers incorrect")
			w.WriteHeader(400)
			return
		}
		upstream, err := net.Dial("tcp", target.Host)
		if err != nil {
			t.Error(err)
			return
		}
		downstream, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			t.Error(err)
			return
		}
		fmt.Fprint(downstream, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { io.Copy(upstream, downstream); upstream.Close(); downstream.Close() }()
		go func() { io.Copy(downstream, upstream); upstream.Close(); downstream.Close() }()
	}))
	defer proxy.Close()
	c := localTLSClient(t, server, "one")
	u, _ := url.Parse(proxy.URL)
	u.User = url.UserPassword("user", "pass")
	tr := c.HTTP.Transport.(*http.Transport)
	tr.Proxy = http.ProxyURL(u)
	tr.ProxyConnectHeader = http.Header{"X-Test": {"connect"}}
	awaitPreconnect(t, c)
	if platformCalls.Load() != 0 || proxyCalls.Load() != 1 {
		t.Fatal("prewarm sent platform request")
	}
	body, err := c.getUsing(context.Background(), "/User/CheckUserStatus", nil, true)
	if err != nil || string(body) != "1" || proxyCalls.Load() != 1 || platformCalls.Load() != 1 {
		t.Fatal(err, string(body), proxyCalls.Load(), platformCalls.Load())
	}
}

func TestPreconnectProxyDenialDoesNotReachPlatform(t *testing.T) {
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { connects.Add(1); w.WriteHeader(407) }))
	defer proxy.Close()
	c := newClient(Session{}, time.Second)
	u, _ := url.Parse(proxy.URL)
	c.HTTP.Transport.(*http.Transport).Proxy = http.ProxyURL(u)
	defer c.closeOrderPreconnect()
	p := c.startOrderPreconnect(context.Background())
	<-p.done
	if p.conn != nil || connects.Load() != 1 {
		t.Fatal("proxy denial ignored")
	}
}
