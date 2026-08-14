package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve runs h behind withGzip and returns the recorded response.
func serve(t *testing.T, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	withGzip(h).ServeHTTP(rec, req)
	return rec
}

func gzipReq(target string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	return r
}

func TestGzipCompressesHTML(t *testing.T) {
	body := strings.Repeat("<p>artifact</p>", 500)
	rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, body)
	}, gzipReq("/a/big/"))

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Error("Content-Length must be dropped: it described the identity body")
	}
	if v := rec.Header().Values("Vary"); len(v) != 1 || v[0] != "Accept-Encoding" {
		t.Errorf("Vary = %v, want [Accept-Encoding]", v)
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("compressed body %d bytes, not smaller than %d", rec.Body.Len(), len(body))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Error("round-tripped body does not match the original")
	}
}

func TestGzipSkippedWithoutAcceptEncoding(t *testing.T) {
	rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "hello")
	}, httptest.NewRequest("GET", "/a/x/", nil))

	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("compressed a client that never offered gzip")
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want hello", rec.Body.String())
	}
	// Caches still need to know the response would have varied.
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Error("Vary must be set even when the response is not compressed")
	}
}

// The SSE handler type-asserts http.Flusher and fails the request outright
// without it, which would take live refresh down across the whole site.
func TestGzipPreservesFlusherAndSkipsEventStream(t *testing.T) {
	flushed := false
	rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapped ResponseWriter lost http.Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": connected\n\n")
		f.Flush()
		flushed = true
	}, gzipReq("/events"))

	if !flushed {
		t.Fatal("handler never completed")
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("event streams must not be compressed: gzip buffering stalls them")
	}
	if rec.Body.String() != ": connected\n\n" {
		t.Errorf("body = %q, want the raw SSE frame", rec.Body.String())
	}
}

func TestGzipSkipsRangeRequests(t *testing.T) {
	req := gzipReq("/a/vid/clip.mp4")
	req.Header.Set("Range", "bytes=0-99")
	rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "partial")
	}, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("compressed a Range response, renumbering the byte offsets")
	}
	if rec.Body.String() != "partial" {
		t.Errorf("body = %q, want partial", rec.Body.String())
	}
}

func TestGzipSkipsAlreadyCompressedTypes(t *testing.T) {
	for _, ct := range []string{"image/png", "video/mp4", "font/woff2", "application/zip"} {
		rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			io.WriteString(w, strings.Repeat("x", 4096))
		}, gzipReq("/a/x/asset"))
		if rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: re-compressed an already-compressed format", ct)
		}
	}
}

func TestGzipSkipsNonOKStatus(t *testing.T) {
	// 304 carries no body; encoding it would contradict the cached entity.
	rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotModified)
	}, gzipReq("/a/x/"))

	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("compressed a 304")
	}
	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
}

// A handler that writes without calling WriteHeader relies on net/http's
// sniffing; the wrapper has to resolve the type the same way before deciding.
func TestGzipSniffsContentTypeOnBareWrite(t *testing.T) {
	rec := serve(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<!DOCTYPE html><title>x</title>"+strings.Repeat(" ", 2000))
	}, gzipReq("/a/x/"))

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip (sniffed as html)", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want text/html", rec.Header().Get("Content-Type"))
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"gzip":                         true,
		"gzip, deflate, br":            true,
		"deflate, gzip;q=1.0, *;q=0.5": true,
		"GZIP":                         true,
		"":                             false,
		"deflate, br":                  false,
		"gzip;q=0":                     false,
		// must not match a different encoding that merely contains "gzip"
		"x-gzip-ish": false,
	}
	for header, want := range cases {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestCompressible(t *testing.T) {
	cases := map[string]bool{
		"text/html; charset=utf-8": true,
		"text/css":                 true,
		"application/json":         true,
		"image/svg+xml":            true,
		"text/event-stream":        false,
		"image/png":                false,
		"application/octet-stream": false,
		"":                         false,
	}
	for ct, want := range cases {
		if got := compressible(ct); got != want {
			t.Errorf("compressible(%q) = %v, want %v", ct, got, want)
		}
	}
}
