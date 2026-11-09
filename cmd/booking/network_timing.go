// Request phases only: never log URLs, addresses, headers, cookies or TLS tickets.
package main

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

type requestTiming struct {
	mu                                          sync.Mutex
	started, dns, handshake, connected, written time.Time
	connects                                    map[string]time.Time
	values                                      map[string]any
}

func newRequestTiming(started time.Time) *requestTiming {
	return &requestTiming{started: started, connects: map[string]time.Time{}, values: map[string]any{}}
}

func elapsedMS(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func (n *requestTiming) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { n.mu.Lock(); defer n.mu.Unlock(); n.dns = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			n.mu.Lock()
			defer n.mu.Unlock()
			if !n.dns.IsZero() {
				n.values["dnsMs"] = elapsedMS(n.dns)
			}
		},
		ConnectStart: func(network, address string) {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.connects[network+address] = time.Now()
		},
		ConnectDone: func(network, address string, err error) {
			n.mu.Lock()
			defer n.mu.Unlock()
			if t, ok := n.connects[network+address]; ok && err == nil {
				n.values["connectMs"] = elapsedMS(t)
			}
			delete(n.connects, network+address)
		},
		TLSHandshakeStart: func() { n.mu.Lock(); defer n.mu.Unlock(); n.handshake = time.Now() },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			n.mu.Lock()
			defer n.mu.Unlock()
			if !n.handshake.IsZero() {
				n.values["tlsMs"] = elapsedMS(n.handshake)
			}
			if err == nil {
				n.values["tlsResumed"] = state.DidResume
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.connected = time.Now()
			n.values["connectionReused"] = info.Reused
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.written = time.Now()
			if !n.connected.IsZero() {
				n.values["writeMs"] = elapsedMS(n.connected)
			}
			count, _ := n.values["writeAttempts"].(int)
			n.values["writeAttempts"] = count + 1
		},
		GotFirstResponseByte: func() {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.values["ttfbMs"] = elapsedMS(n.started)
			if !n.written.IsZero() {
				n.values["responseWaitMs"] = elapsedMS(n.written)
			}
		},
	}
}

func (n *requestTiming) snapshot() map[string]any {
	n.mu.Lock()
	defer n.mu.Unlock()
	values := make(map[string]any, len(n.values))
	for k, v := range n.values {
		values[k] = v
	}
	return values
}
