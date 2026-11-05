package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPBoundaries(t *testing.T) {
	for _, status := range []int{302, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := fake(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "9")
				w.Header().Set("Location", "/secret")
				w.WriteHeader(status)
			})
			_, e := c.get(context.Background(), "/x", nil)
			if e == nil {
				t.Fatal("accepted", status)
			}
			if strings.Contains(e.Error(), "secret") {
				t.Fatal("secret leaked")
			}
			if status == 429 && e.(*Fault).RetryAfter != 9*time.Second {
				t.Fatal(e)
			}
		})
	}
}
