package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMutationsResumeTLSOnFreshHTTP1Connections(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			requests := 0
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/warm" && (r.ProtoMajor != 1 || !r.Close || !r.TLS.DidResume) {
					t.Errorf("mutation did not use fresh HTTP1 with TLS resumption: protocol=%s close=%v resume=%v", r.Proto, r.Close, r.TLS.DidResume)
				}
				fmt.Fprint(w, `{"type":1}`)
			}))
			server.EnableHTTP2 = true
			server.TLS = &tls.Config{MinVersion: version, MaxVersion: version}
			server.StartTLS()
			defer server.Close()
			c := newClient(Session{Cookie: "UserId=synthetic; session=private-test-cookie"}, time.Second)
			c.Base = server.URL
			defer c.HTTP.CloseIdleConnections()
			pool := x509.NewCertPool()
			pool.AddCert(server.Certificate())
			tr := c.HTTP.Transport.(*http.Transport)
			tr.TLSClientConfig.RootCAs = pool
			tr.TLSClientConfig.MinVersion = version
			tr.TLSClientConfig.MaxVersion = version
			c.EventLog = filepath.Join(t.TempDir(), "events.jsonl")
			for _, path := range []string{"/warm", "/Field/OrderField", "/Field/FieldCancel"} {
				if _, err := c.get(context.Background(), path, nil); err != nil {
					t.Fatal(err)
				}
			}
			if requests != 3 {
				t.Fatal("unexpected request count", requests)
			}
			data, _ := os.ReadFile(c.EventLog)
			if strings.Contains(string(data), "private-test-cookie") || strings.Contains(string(data), server.URL) {
				t.Fatal("private data in timing log")
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			for _, line := range lines[1:] {
				var row map[string]any
				json.Unmarshal([]byte(line), &row)
				network := row["network"].(map[string]any)
				if network["tlsResumed"] != true || network["connectionReused"] != false || network["writeAttempts"] != float64(1) {
					t.Fatal(network)
				}
				for _, field := range []string{"connectMs", "tlsMs", "ttfbMs", "responseWaitMs"} {
					if _, ok := network[field]; !ok {
						t.Fatal("missing timing", field)
					}
				}
			}
		})
	}
}

func TestMutationLostResponseIsNotReplayed(t *testing.T) {
	for _, endpoint := range []string{"/Field/OrderField", "/Field/OrderFieldFree", "/Field/FieldCancel"} {
		t.Run(endpoint, func(t *testing.T) {
			requests := 0
			c := fake(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Fatal(err)
				}
				conn.Close()
			})
			_, err := c.get(context.Background(), endpoint, nil)
			if faultKind(err) != "network_error" || requests != 1 {
				t.Fatal(err, requests)
			}
		})
	}
}
