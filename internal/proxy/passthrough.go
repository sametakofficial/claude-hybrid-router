package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
)

// passthrough sends the request to Anthropic with the client's own headers
// (the subscription's OAuth token stays between Claude Code and Anthropic).
func (p *Proxy) passthrough(w http.ResponseWriter, r *http.Request, body []byte, e stats.Entry, countOnly bool) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	if countOnly {
		p.up.ServeHTTP(w, r)
		return
	}
	sw := anthropic.NewUsageWriter(w)
	start := time.Now()
	finished := false
	// ReverseProxy panics with http.ErrAbortHandler when a stream is cut
	// mid-way. The entry is still recorded; the panic goes on to net/http,
	// which closes the connection.
	defer func() {
		e.Status, e.LatencyMS = sw.Status(), time.Since(start).Milliseconds()
		setUsage(&e, sw.Usage())
		switch {
		case !finished:
			e.Error = "stream interrupted"
		case e.Status >= 400:
			e.Error = sw.ErrText()
			if e.Status == http.StatusTooManyRequests {
				e.Error = rateLimitInfo(sw.Header(), e.Error)
			}
		}
		p.rec.Add(e)
	}()
	p.up.ServeHTTP(sw, r)
	finished = true
}

// rateLimitInfo explains an Anthropic 429 using its response headers.
func rateLimitInfo(h http.Header, msg string) string {
	parts := []string{"Anthropic rate limit (Claude Code retries automatically)"}
	if msg != "" && msg != "Error" {
		parts = append(parts, msg)
	}
	for _, k := range []string{"retry-after", "anthropic-ratelimit-unified-status", "anthropic-ratelimit-unified-reset", "anthropic-ratelimit-unified-representative-claim"} {
		if v := h.Get(k); v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}
