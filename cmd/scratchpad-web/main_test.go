package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerTimeoutsPreserveStreaming(t *testing.T) {
	s := newHTTPServer("127.0.0.1:8737", http.NotFoundHandler())
	if s.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", s.ReadHeaderTimeout)
	}
	if s.IdleTimeout != 120*time.Second {
		t.Fatalf("IdleTimeout = %s", s.IdleTimeout)
	}
	if s.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want zero for SSE", s.WriteTimeout)
	}
}
