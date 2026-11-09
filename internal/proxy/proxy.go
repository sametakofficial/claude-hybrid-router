// Package proxy is the HTTP server Claude Code talks to (via
// ANTHROPIC_BASE_URL). It passes normal traffic to Anthropic and routes
// marked subagents and matching models to other providers.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/provider"
	"github.com/sametakofficial/claude-hybrid-router/internal/router"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
)

const (
	maxBody = 64 << 20
	// HealthPath answers on the proxy port so the launcher can tell it is ours.
	HealthPath = "/_claude-hybrid/health"
)

// Proxy implements http.Handler.
type Proxy struct {
	cfg    func() *config.Config
	rec    *stats.Recorder
	client *provider.Client
	up     *httputil.ReverseProxy

	// OnUpstreamHeaders, if set, sees the response headers of every
	// Anthropic /v1/messages reply (used to read subscription rate limits).
	OnUpstreamHeaders func(http.Header)
}

// New builds a proxy. cfg must return the live config (treated read-only).
func New(cfg func() *config.Config, rec *stats.Recorder) *Proxy {
	transport := provider.NewTransport(time.Duration(cfg().Server.HeaderTimeout) * time.Second)
	p := &Proxy{cfg: cfg, rec: rec, client: provider.NewClient(transport)}
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
			anthropic.WriteError(w, http.StatusBadGateway, "claude-hybrid: Anthropic upstream unreachable: "+err.Error())
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
		anthropic.WriteError(w, http.StatusForbidden, "claude-hybrid: browser and non-local requests are not accepted")
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
		anthropic.WriteError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(body) > maxBody {
		anthropic.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	cfg := p.cfg()
	d, err := router.Decide(cfg, body)
	req := d.Req
	countOnly := strings.HasSuffix(r.URL.Path, "/count_tokens")
	entry := stats.Entry{Time: time.Now(), Path: r.URL.Path, Source: d.Source()}
	if req != nil {
		entry.ReqModel, entry.Stream = req.Model(), req.Stream()
	}
	if err != nil || d.Subscription() {
		// Anthropic subscription: forward untouched (minus the marker).
		entry.Kind = "anthropic"
		p.passthrough(w, r, d.Body, entry, countOnly)
		return
	}

	entry.Kind = "routed"
	switch {
	case d.Via == router.ViaLegacy:
		entry.Provider = "legacy"
		entry.Model = entry.ReqModel
		prov := config.Provider{ID: "legacy", Type: config.TypeAnthropic, BaseURL: d.LegacyURL}
		p.routed(w, r, prov, d.Route, req, entry, countOnly)
	default:
		prov, ok := cfg.Provider(d.Route.Provider)
		entry.Provider = d.Route.Provider
		entry.Model = d.Route.Model
		if entry.Model == "" {
			entry.Model = entry.ReqModel
		}
		if !ok || !prov.Enabled {
			msg := fmt.Sprintf("claude-hybrid: provider %q for %s is missing or disabled", d.Route.Provider, d.Source())
			p.fail(w, entry, http.StatusBadRequest, msg, countOnly)
			return
		}
		router.Apply(req, d.Route)
		if prov.Type == config.TypeOpenAI {
			p.toOpenAI(w, r, prov, d.Route, req, entry, countOnly)
		} else {
			p.routed(w, r, prov, d.Route, req, entry, countOnly)
		}
	}
}

func (p *Proxy) fail(w http.ResponseWriter, e stats.Entry, code int, msg string, countOnly bool) {
	anthropic.WriteError(w, code, msg)
	if !countOnly {
		e.Status, e.Error = code, msg
		p.rec.Add(e)
	}
}

func setUsage(e *stats.Entry, u anthropic.Usage) {
	e.InputTokens, e.OutputTokens = u.Input, u.Output
	e.CacheReadTokens, e.CacheWriteTokens = u.CacheRead, u.CacheWrite
}
