// Package panel serves the web UI and its JSON API.
package panel

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/provider"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
	"github.com/sametakofficial/claude-hybrid-router/internal/usage"
)

//go:embed web/index.html
var webFS embed.FS

// Panel holds what the API needs.
type Panel struct {
	Store   *config.Store
	Rec     *stats.Recorder
	Usage   *usage.Store // optional
	Version string
	Started time.Time
	// Client is used to talk to providers (model list, Test button). Nil
	// means a default client. Tests set one that cannot leave loopback.
	Client *http.Client
}

var defaultClient = provider.NewClient(provider.NewTransport(60 * time.Second))

func (p *Panel) client() *provider.Client {
	if p.Client != nil {
		c := provider.NewClient(p.Client.Transport)
		c.Retries = 0
		return c
	}
	return defaultClient
}

type apiError struct {
	code int
	msg  string
}

func (e apiError) Error() string { return e.msg }

func bad(format string, a ...any) error {
	return apiError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

func notFound(format string, a ...any) error {
	return apiError{http.StatusNotFound, fmt.Sprintf(format, a...)}
}

type handlerFunc func(r *http.Request) (any, error)

// Handler returns the panel's HTTP handler.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	page, _ := webFS.ReadFile("web/index.html")
	devDir := os.Getenv("CLAUDE_HYBRID_WEB_DIR") // serve the UI from disk while editing it
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page := page
		if devDir != "" {
			if b, err := os.ReadFile(filepath.Join(devDir, "index.html")); err == nil {
				page = b
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Write(page)
	})
	route := func(pattern string, h handlerFunc) { mux.Handle(pattern, p.api(h)) }

	route("GET /api/status", p.status)
	route("GET /api/usage", p.usageReport)
	route("GET /api/presets", func(*http.Request) (any, error) { return presets, nil })
	route("GET /api/providers", p.listProviders)
	route("PUT /api/providers/{id}", p.putProvider)
	route("PUT /api/providers/{id}/enabled", p.putProviderEnabled)
	route("DELETE /api/providers/{id}", p.deleteProvider)
	route("POST /api/providers/{id}/test", p.testProvider)
	route("POST /api/providers/{id}/refresh", p.refreshModels)
	route("POST /api/discover", p.discoverModels)
	route("GET /api/meta", func(*http.Request) (any, error) { return meta, nil })
	route("GET /api/agents", p.listAgents)
	route("PUT /api/agents/{name}", p.putAgent)
	route("PUT /api/agents/{name}/route", p.putAgentRoute)
	route("DELETE /api/agents/{name}", p.deleteAgent)
	route("GET /api/rules", func(*http.Request) (any, error) { return nonNil(p.Store.Current().Rules), nil })
	route("PUT /api/rules", p.putRules)
	route("GET /api/settings", func(*http.Request) (any, error) { return maskSettings(p.Store.Current().Server), nil })
	route("PUT /api/settings", p.putSettings)
	route("GET /api/logs", p.logs)
	route("GET /api/config/raw", p.getRaw)
	route("PUT /api/config/raw", p.putRaw)
	route("POST /api/reload", func(*http.Request) (any, error) {
		if err := p.Store.Reload(); err != nil {
			return nil, bad("reload: %v", err)
		}
		return map[string]bool{"ok": true}, nil
	})
	return p.guard(mux)
}

// guard blocks the two ways a web page in the browser could reach a
// localhost API: DNS rebinding (checked via Host) and cross-site requests
// (checked via the custom X-CHR header, which forces a CORS preflight that we
// never approve). With a token set, the token is required as well.
func (p *Panel) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := p.Store.Current().Server.PanelToken
		if token == "" && !isLoopbackHost(r.Host) {
			http.Error(w, "claude-hybrid panel: set server.panel_token to allow non-localhost access", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Header.Get("X-CHR") != "1" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-CHR header"})
				return
			}
			if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CHR-Token")), []byte(token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "panel token required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *Panel) api(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		out, err := h(r)
		if err != nil {
			code := http.StatusInternalServerError
			var ae apiError
			var ve *config.ValidationError
			switch {
			case errors.As(err, &ae):
				code = ae.code
			case errors.As(err, &ve):
				code = http.StatusBadRequest
			}
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return bad("invalid JSON: %v", err)
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
