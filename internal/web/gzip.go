package web

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// Artifacts are served verbatim from disk, and a self-contained page that
// inlines its own data can run to megabytes — a generated graph or report is
// mostly repeated JSON, which gzip shrinks by an order of magnitude. Nothing
// here is cached between requests, so the win is entirely on the wire, which
// is what matters when the site is opened from another machine on the LAN
// rather than from localhost.
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Announce the negotiation even when we end up not compressing, so a
		// proxy in front of us can never serve one encoding for the other.
		w.Header().Add("Vary", "Accept-Encoding")
		// A Range request wants byte offsets into the file on disk; encoding
		// the body would renumber them, so those responses stay untouched
		// (they are media seeks, which are already-compressed formats anyway).
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// acceptsGzip reports whether the header offers gzip at a non-zero q-value.
func acceptsGzip(header string) bool {
	explicit, wildcard := -1.0, -1.0
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		coding := strings.TrimSpace(fields[0])
		if !strings.EqualFold(coding, "gzip") && coding != "*" {
			continue
		}
		q, valid := 1.0, true
		for _, p := range fields[1:] {
			if k, v, ok := strings.Cut(p, "="); ok &&
				strings.EqualFold(strings.TrimSpace(k), "q") {
				var ok bool
				q, ok = parseQuality(strings.TrimSpace(v))
				if !ok {
					valid = false
				}
			}
		}
		if !valid {
			continue
		}
		if strings.EqualFold(coding, "gzip") {
			explicit = max(explicit, q)
		} else {
			wildcard = max(wildcard, q)
		}
	}
	if explicit >= 0 {
		return explicit > 0
	}
	return wildcard > 0
}

func parseQuality(value string) (float64, bool) {
	whole, fraction, dotted := strings.Cut(value, ".")
	if whole != "0" && whole != "1" {
		return 0, false
	}
	if dotted {
		if len(fraction) > 3 {
			return 0, false
		}
		for _, digit := range fraction {
			if digit < '0' || digit > '9' || whole == "1" && digit != '0' {
				return 0, false
			}
		}
	}
	q, err := strconv.ParseFloat(value, 64)
	return q, err == nil
}

// gzipWriter defers the compress-or-not choice to the moment the handler
// commits a status, which is the first point where Content-Type is settled —
// handlers here set it at the last minute (or leave it to sniffing), so
// deciding any earlier would misjudge streams and binary files.
type gzipWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	decided  bool
	compress bool
}

func (w *gzipWriter) WriteHeader(status int) {
	w.decide(status)
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.decided {
		// Mirror net/http: an uncommitted response sniffs its own type, and
		// we need that type resolved before deciding.
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.decide(http.StatusOK)
	}
	if w.compress {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush must exist for the SSE handler, which type-asserts http.Flusher and
// fails the request outright without it. Event streams are never compressed,
// but flushing the gzip writer first keeps the contract honest if that ever
// changes.
func (w *gzipWriter) Flush() {
	if w.compress {
		w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	// 204/304 carry no body, and a handler that set its own encoding (or
	// serves an already-compressed format) is left alone.
	if status != http.StatusOK || h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		return
	}
	w.compress = true
	// The length and range semantics both describe the identity body.
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	h.Set("Content-Encoding", "gzip")
	w.gz = gzip.NewWriter(w.ResponseWriter)
}

func (w *gzipWriter) close() {
	if w.gz != nil {
		w.gz.Close()
	}
}

// compressible reports whether a media type is worth gzipping. Images, video,
// fonts and archives are already compressed; re-encoding them costs CPU and
// usually grows the payload.
func compressible(contentType string) bool {
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.TrimSpace(strings.ToLower(ct))
	// text/event-stream is a live stream: buffering it into gzip blocks would
	// stall the change notifications the whole site refreshes on.
	if ct == "text/event-stream" {
		return false
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/javascript", "application/xml",
		"application/xhtml+xml", "application/rss+xml", "application/atom+xml",
		"application/wasm", "image/svg+xml":
		return true
	}
	return false
}
