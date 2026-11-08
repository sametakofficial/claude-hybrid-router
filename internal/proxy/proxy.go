// Package proxy is the HTTP server Claude Code talks to (via
// ANTHROPIC_BASE_URL). It passes normal traffic to Anthropic and routes
// marked subagents and matching models to other providers.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
	"github.com/sametakofficial/claude-hybrid-router/internal/translate"
)

const (
	maxBody    = 64 << 20
	maxRetries = 2
	// HealthPath answers on the proxy port so the launcher can tell it is ours.
	HealthPath = "/_claude-hybrid/health"
)

// Proxy implements http.Handler.
type Proxy struct {
	cfg    func() *config.Config
	rec    *stats.Recorder
	client *http.Client // for providers
	up     *httputil.ReverseProxy

	// OnUpstreamHeaders, if set, sees the response headers of every
	// Anthropic /v1/messages reply (used to read subscription rate limits).
	OnUpstreamHeaders func(http.Header)
}

// New builds a proxy. cfg must return the live config (treated read-only).
func New(cfg func() *config.Config, rec *stats.Recorder) *Proxy {
	timeout := time.Duration(cfg().Server.HeaderTimeout) * time.Second
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	p := &Proxy{cfg: cfg, rec: rec, client: &http.Client{
		Transport: transport,
		// Never follow redirects with a provider key attached.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	p.up = &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // flush every write: SSE must not be buffered
		Rewrite: func(pr *httputil.ProxyRequest) {
			u, err := url.Parse(p.cfg().Server.Upstream)
			if err != nil {
				u, _ = url.Parse("https://api.anthropic.com")
			}
			pr.SetURL(u)
			pr.Out.Host = u.Host
			// Let Go's transport handle compression so responses are plain
			// text here and token usage can be read for the stats.
			pr.Out.Header.Del("Accept-Encoding")
		},
		// Headers are read here, before the body is copied, so a stream the
		// client aborts still reports its rate-limit state.
		ModifyResponse: func(resp *http.Response) error {
			if p.OnUpstreamHeaders != nil && strings.HasPrefix(resp.Request.URL.Path, "/v1/messages") {
				p.OnUpstreamHeaders(resp.Header)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("upstream error %s %s: %v", r.Method, r.URL.Path, err)
			writeError(w, http.StatusBadGateway, "api_error", "claude-hybrid: Anthropic upstream unreachable: "+err.Error())
		},
	}
	return p
}

// fromBrowser reports requests that a web page sent (Claude Code never sets
// Origin or Sec-Fetch-*), and requests whose Host is not loopback (DNS
// rebinding). Without this any open web page could use the provider keys.
func fromBrowser(r *http.Request) bool {
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if fromBrowser(r) {
		writeError(w, http.StatusForbidden, "permission_error", "claude-hybrid: browser and non-local requests are not accepted")
		return
	}
	if r.URL.Path == HealthPath {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok","app":"claude-hybrid"}`)
		return
	}
	isMessages := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/messages")
	if !isMessages {
		p.up.ServeHTTP(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	r.Body.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "read body: "+err.Error())
		return
	}
	if len(body) > maxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
		return
	}

	cfg := p.cfg()
	d, req, err := decide(cfg, body)
	countOnly := strings.HasSuffix(r.URL.Path, "/count_tokens")
	entry := stats.Entry{Time: time.Now(), Path: r.URL.Path, Source: d.Source}
	if req != nil {
		entry.ReqModel, entry.Stream = req.model(), req.stream()
	}
	if err != nil || (d.Route.IsAnthropic() && d.LegacyURL == "") {
		// Anthropic subscription: forward untouched (minus the marker).
		entry.Kind = "anthropic"
		p.passthrough(w, r, d.Body, entry, countOnly)
		return
	}

	entry.Kind = "routed"
	switch {
	case d.LegacyURL != "":
		entry.Provider = "legacy"
		entry.Model = entry.ReqModel
		prov := config.Provider{ID: "legacy", Type: config.TypeAnthropic, BaseURL: d.LegacyURL}
		p.toAnthropic(w, r, prov, d.Route, req, entry, countOnly)
	default:
		prov, ok := cfg.Provider(d.Route.Provider)
		entry.Provider = d.Route.Provider
		entry.Model = d.Route.Model
		if entry.Model == "" {
			entry.Model = entry.ReqModel
		}
		if !ok || !prov.Enabled {
			msg := fmt.Sprintf("claude-hybrid: provider %q for %s is missing or disabled", d.Route.Provider, d.Source)
			p.fail(w, entry, http.StatusBadRequest, msg, countOnly)
			return
		}
		applyRoute(req, d.Route)
		if prov.Type == config.TypeAnthropic {
			p.toAnthropic(w, r, prov, d.Route, req, entry, countOnly)
		} else {
			p.toOpenAI(w, r, prov, d.Route, req, entry, countOnly)
		}
	}
}

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
	sw := &statusWriter{ResponseWriter: w}
	start := time.Now()
	p.up.ServeHTTP(sw, r)
	e.Status, e.LatencyMS = sw.code(), time.Since(start).Milliseconds()
	e.InputTokens, e.OutputTokens = sw.usage.in, sw.usage.out
	e.CacheReadTokens, e.CacheWriteTokens = sw.usage.cacheRead, sw.usage.cacheWrite
	if e.Status >= 400 {
		e.Error = sw.errText()
		if e.Status == http.StatusTooManyRequests {
			e.Error = rateLimitInfo(sw.Header(), e.Error)
		}
	}
	p.rec.Add(e)
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

func (p *Proxy) fail(w http.ResponseWriter, e stats.Entry, code int, msg string, countOnly bool) {
	writeError(w, code, errType(code), msg)
	if !countOnly {
		e.Status, e.Error = code, msg
		p.rec.Add(e)
	}
}

// do sends a provider request with retries for connection errors and 5xx.
// Retries only happen before any response bytes reach the client.
func (p *Proxy) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(300<<attempt) * time.Millisecond):
			}
		}
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := p.client.Do(req.WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 && resp.StatusCode != 501 && attempt < maxRetries {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func setProviderHeaders(h http.Header, prov config.Provider) {
	for k, v := range prov.Headers {
		h.Set(k, v)
	}
}

// toAnthropic forwards to an Anthropic-compatible provider. Client headers are
// NOT copied, so the subscription token never leaves for a third party.
func (p *Proxy) toAnthropic(w http.ResponseWriter, r *http.Request, prov config.Provider, rt config.Route, req request, e stats.Entry, countOnly bool) {
	if rt.Model != "" {
		req["model"] = rt.Model
	}
	if rt.MaxTokens > 0 {
		if n, err := strconv.Atoi(fmt.Sprint(req["max_tokens"])); err != nil || n > rt.MaxTokens {
			req["max_tokens"] = rt.MaxTokens
		}
	}
	if rt.Temperature != nil {
		req["temperature"] = *rt.Temperature
	}
	body, err := json.Marshal(req)
	if err != nil {
		p.fail(w, e, http.StatusBadRequest, "claude-hybrid: encode request: "+err.Error(), countOnly)
		return
	}
	base := strings.TrimRight(prov.BaseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	target := base + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	version := r.Header.Get("anthropic-version")
	if version == "" {
		version = "2023-06-01"
	}
	start := time.Now()
	resp, err := p.do(r.Context(), func() (*http.Request, error) {
		out, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		out.Header.Set("Content-Type", "application/json")
		out.Header.Set("anthropic-version", version)
		if key := prov.Key(); key != "" {
			out.Header.Set("x-api-key", key)
			out.Header.Set("Authorization", "Bearer "+key)
		}
		setProviderHeaders(out.Header, prov)
		return out, nil
	})
	if err != nil {
		if countOnly {
			writeCountEstimate(w, body)
			return
		}
		p.fail(w, e, http.StatusBadGateway, fmt.Sprintf("claude-hybrid: provider %q unreachable: %v", prov.ID, err), countOnly)
		return
	}
	defer resp.Body.Close()
	if countOnly && resp.StatusCode != http.StatusOK {
		writeCountEstimate(w, body) // many compatible APIs lack count_tokens
		return
	}
	if resp.StatusCode != http.StatusOK {
		p.providerError(w, e, prov, resp, countOnly)
		return
	}
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(http.StatusOK)
	sw := &statusWriter{ResponseWriter: w, status: 200, sse: strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")}
	_, copyErr := copyFlush(sw, resp.Body)
	if countOnly {
		return
	}
	e.Status, e.LatencyMS = sw.code(), time.Since(start).Milliseconds()
	e.InputTokens, e.OutputTokens = sw.usage.in, sw.usage.out
	e.CacheReadTokens, e.CacheWriteTokens = sw.usage.cacheRead, sw.usage.cacheWrite
	if copyErr != nil && r.Context().Err() == nil {
		e.Error = "stream interrupted: " + copyErr.Error()
	}
	p.rec.Add(e)
}

// toOpenAI translates to Chat Completions and back.
func (p *Proxy) toOpenAI(w http.ResponseWriter, r *http.Request, prov config.Provider, rt config.Route, req request, e stats.Entry, countOnly bool) {
	raw, err := json.Marshal(req)
	if err != nil {
		p.fail(w, e, http.StatusBadRequest, "claude-hybrid: encode request: "+err.Error(), countOnly)
		return
	}
	if countOnly {
		writeCountEstimate(w, raw)
		return
	}
	body, err := translate.ToOpenAI(raw, translate.Options{
		Model: rt.Model, MaxTokensCap: rt.MaxTokens, MaxTokensField: prov.MaxTokensField,
		Temperature: rt.Temperature, ReasoningEffort: rt.ReasoningEffort,
	})
	if err != nil {
		p.fail(w, e, http.StatusBadRequest, "claude-hybrid: translate request: "+err.Error(), false)
		return
	}
	target := strings.TrimRight(prov.BaseURL, "/") + "/chat/completions"
	start := time.Now()
	resp, err := p.do(r.Context(), func() (*http.Request, error) {
		out, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		out.Header.Set("Content-Type", "application/json")
		if key := prov.Key(); key != "" {
			out.Header.Set("Authorization", "Bearer "+key)
		}
		setProviderHeaders(out.Header, prov)
		return out, nil
	})
	if err != nil {
		p.fail(w, e, http.StatusBadGateway, fmt.Sprintf("claude-hybrid: provider %q unreachable: %v", prov.ID, err), false)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.providerError(w, e, prov, resp, false)
		return
	}
	// The model name shown to Claude Code: keep what it asked for so its
	// own bookkeeping (context window, pricing labels) stays sane.
	shownModel := e.ReqModel
	isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	if e.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		src := io.Reader(resp.Body)
		if !isSSE { // provider ignored stream=true: wrap the JSON as one chunk
			src = jsonAsSSE(resp.Body)
		}
		usage, err := translate.NewStream(w, flush, shownModel).Run(src)
		e.Status, e.LatencyMS = 200, time.Since(start).Milliseconds()
		e.InputTokens, e.OutputTokens = usage.InputTokens+usage.CacheRead, usage.OutputTokens
		e.CacheReadTokens = usage.CacheRead
		if err != nil && r.Context().Err() == nil {
			e.Error = err.Error()
		}
		p.rec.Add(e)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		p.fail(w, e, http.StatusBadGateway, "claude-hybrid: read provider response: "+err.Error(), false)
		return
	}
	out, usage, err := translate.FromOpenAI(data, shownModel)
	if err != nil {
		p.fail(w, e, http.StatusBadGateway, fmt.Sprintf("claude-hybrid: provider %q: %v", prov.ID, err), false)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
	e.Status, e.LatencyMS = 200, time.Since(start).Milliseconds()
	e.InputTokens, e.OutputTokens = usage.InputTokens+usage.CacheRead, usage.OutputTokens
	e.CacheReadTokens = usage.CacheRead
	p.rec.Add(e)
}

// providerError turns a provider failure into an Anthropic-style error.
// 401/403 are mapped to 400: passing them on would make Claude Code think
// the user's own Anthropic login expired.
func (p *Proxy) providerError(w http.ResponseWriter, e stats.Entry, prov config.Provider, resp *http.Response, countOnly bool) {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	detail := redact(strings.TrimSpace(string(data)))
	if len(detail) > 1500 {
		detail = detail[:1500] + "…"
	}
	code := resp.StatusCode
	switch {
	case code == http.StatusTooManyRequests:
	case code >= 500:
		code = http.StatusBadGateway
	default:
		code = http.StatusBadRequest
	}
	msg := fmt.Sprintf("claude-hybrid: provider %q returned HTTP %d: %s", prov.ID, resp.StatusCode, detail)
	log.Printf("provider error: %s", msg)
	p.fail(w, e, code, msg, countOnly)
}

func errType(code int) string {
	switch {
	case code == http.StatusTooManyRequests:
		return "rate_limit_error"
	case code >= 500:
		return "api_error"
	}
	return "invalid_request_error"
}

func writeError(w http.ResponseWriter, code int, typ, msg string) {
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(b)
}

// writeCountEstimate answers count_tokens locally (about 4 chars per token).
func writeCountEstimate(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"input_tokens":%d}`, len(body)/4+1)
}

var redactRE = regexp.MustCompile(`(?i)(bearer\s+|sk-|key-)[A-Za-z0-9._\-]{8,}`)

func redact(s string) string { return redactRE.ReplaceAllString(s, "${1}[REDACTED]") }

// copyFlush copies src to dst, flushing after every read so SSE stays live.
func copyFlush(dst io.Writer, src io.Reader) (int64, error) {
	f, _ := dst.(http.Flusher)
	buf := make([]byte, 32<<10)
	var n int64
	for {
		k, err := src.Read(buf)
		if k > 0 {
			if _, werr := dst.Write(buf[:k]); werr != nil {
				return n, werr
			}
			n += int64(k)
			if f != nil {
				f.Flush()
			}
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// jsonAsSSE wraps a non-streaming Chat Completions JSON reply as a stream of
// one chunk, for providers that ignore stream=true.
func jsonAsSSE(r io.Reader) io.Reader {
	data, _ := io.ReadAll(io.LimitReader(r, maxBody))
	var full struct {
		ID      string `json:"id"`
		Choices []struct {
			Message      json.RawMessage `json:"message"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(data, &full) != nil || len(full.Choices) == 0 {
		return bytes.NewReader(append(append([]byte("data: "), data...), "\n\ndata: [DONE]\n\n"...))
	}
	chunk := map[string]any{
		"id":      full.ID,
		"choices": []any{map[string]any{"delta": full.Choices[0].Message, "finish_reason": full.Choices[0].FinishReason}},
	}
	if len(full.Usage) > 0 {
		chunk["usage"] = full.Usage
	}
	b, _ := json.Marshal(chunk)
	return strings.NewReader("data: " + string(b) + "\n\ndata: [DONE]\n\n")
}
