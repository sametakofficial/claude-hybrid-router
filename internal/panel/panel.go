// Package panel serves the web UI and its JSON API.
package panel

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/agents"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/proxy"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
	"github.com/sametakofficial/claude-hybrid-router/internal/usage"
)

//go:embed web/index.html
var webFS embed.FS

// Panel holds what the API needs.
type Panel struct {
	Store   *config.Store
	Rec     *stats.Recorder
	Proxy   *proxy.Proxy
	Usage   *usage.Store // optional
	Version string
	Started time.Time
	// Client is used to talk to providers (model discovery). Nil means a
	// default client that never follows redirects. Tests set a fake.
	Client *http.Client
}

var defaultClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (p *Panel) client() *http.Client {
	if p.Client != nil {
		return p.Client
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
	route("DELETE /api/providers/{id}", p.deleteProvider)
	route("POST /api/providers/{id}/test", p.testProvider)
	route("POST /api/providers/{id}/refresh", p.refreshModels)
	route("POST /api/discover", p.discoverModels)
	route("GET /api/meta", func(*http.Request) (any, error) { return meta, nil })
	route("GET /api/agents", p.listAgents)
	route("PUT /api/agents/{name}", p.putAgent)
	route("DELETE /api/agents/{name}", p.deleteAgent)
	route("GET /api/rules", func(*http.Request) (any, error) { return nonNil(p.Store.Get().Rules), nil })
	route("PUT /api/rules", p.putRules)
	route("GET /api/settings", func(*http.Request) (any, error) { return maskSettings(p.Store.Get().Server), nil })
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
		token := p.Store.Get().Server.PanelToken
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
			if errors.As(err, &ae) {
				code = ae.code
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

// ---- masking ---------------------------------------------------------------

const maskedToken = "••••"

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 10 {
		return maskedToken
	}
	return k[:3] + "…" + k[len(k)-4:]
}

type providerView struct {
	config.Provider
	HasAPIKey bool `json:"has_api_key"`
}

func viewProvider(pr config.Provider) providerView {
	v := providerView{Provider: pr, HasAPIKey: pr.Key() != ""}
	v.APIKey = maskKey(pr.APIKey)
	if v.Headers == nil {
		v.Headers = map[string]string{}
	}
	v.Models = nonNil(v.Models)
	return v
}

func maskSettings(s config.Server) config.Server {
	if s.PanelToken != "" {
		s.PanelToken = maskedToken
	}
	return s
}

// keepKey returns the stored key when the client sent back nothing or the mask.
func keepKey(sent, stored string) string {
	if sent == "" || sent == maskKey(stored) {
		return stored
	}
	return sent
}

// ---- status & logs ---------------------------------------------------------

var lookPath = exec.LookPath // swapped in tests

func (p *Panel) status(*http.Request) (any, error) {
	cfg := p.Store.Get()
	exe, _ := os.Executable()
	launch := "claude-hybrid"
	if exe != "" {
		if lp, err := lookPath("claude-hybrid"); err != nil || lp != exe {
			launch = exe
		}
	}
	return map[string]any{
		"version":      p.Version,
		"uptime_s":     int(time.Since(p.Started).Seconds()),
		"proxy_listen": cfg.Server.ProxyListen,
		"panel_listen": cfg.Server.PanelListen,
		"config_path":  p.Store.Path(),
		"agents_dir":   cfg.Server.AgentsDir,
		"launch_cmd":   launch,
		"stats":        p.Rec.Summary(),
	}, nil
}

func (p *Panel) logs(r *http.Request) (any, error) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	return p.Rec.Since(since), nil
}

// ---- providers -------------------------------------------------------------

func (p *Panel) listProviders(*http.Request) (any, error) {
	cfg := p.Store.Get()
	out := make([]providerView, 0, len(cfg.Providers))
	for _, pr := range cfg.Providers {
		out = append(out, viewProvider(pr))
	}
	return out, nil
}

func (p *Panel) putProvider(r *http.Request) (any, error) {
	id := r.PathValue("id")
	var in config.Provider
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	in.ID = id
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	models := make([]string, 0, len(in.Models))
	seen := map[string]bool{}
	for _, m := range in.Models {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	in.Models = models
	if len(in.Headers) == 0 {
		in.Headers = nil
	}
	create := r.URL.Query().Get("create") == "1"
	if len(in.Models) == 0 {
		probe := in
		if old, ok := p.Store.Get().Provider(id); ok && !create {
			probe.APIKey = keepKey(in.APIKey, old.APIKey)
		}
		if models, err := p.discover(r.Context(), probe); err == nil {
			in.Models = models
		}
	}
	cfg, err := p.Store.Update(func(c *config.Config) error {
		for i, old := range c.Providers {
			if old.ID == id {
				if create {
					return bad("a provider with id %q already exists", id)
				}
				in.APIKey = keepKey(in.APIKey, old.APIKey)
				c.Providers[i] = in
				return nil
			}
		}
		if in.APIKey == maskedToken {
			in.APIKey = ""
		}
		c.Providers = append(c.Providers, in)
		return nil
	})
	if err != nil {
		return nil, bad("%v", err)
	}
	saved, _ := cfg.Provider(id)
	return viewProvider(saved), nil
}

func (p *Panel) deleteProvider(r *http.Request) (any, error) {
	id := r.PathValue("id")
	_, err := p.Store.Update(func(c *config.Config) error {
		for name, rt := range c.Agents {
			if rt.Provider == id {
				return bad("provider %q is used by agent %q", id, name)
			}
		}
		for i, rule := range c.Rules {
			if rule.Route.Provider == id {
				return bad("provider %q is used by rule %d", id, i+1)
			}
		}
		for i, pr := range c.Providers {
			if pr.ID == id {
				c.Providers = append(c.Providers[:i], c.Providers[i+1:]...)
				return nil
			}
		}
		return notFound("provider %q not found", id)
	})
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (p *Panel) provider(id string) (config.Provider, error) {
	pr, ok := p.Store.Get().Provider(id)
	if !ok {
		return pr, notFound("provider %q not found (save it first)", id)
	}
	return pr, nil
}

// discoverModels fetches a model list for a provider that may not be saved
// yet. A blank or masked key falls back to the stored provider's key.
func (p *Panel) discoverModels(r *http.Request) (any, error) {
	var in config.Provider
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if !strings.HasPrefix(in.BaseURL, "http://") && !strings.HasPrefix(in.BaseURL, "https://") {
		return nil, bad("base URL must start with http:// or https://")
	}
	if old, ok := p.Store.Get().Provider(in.ID); ok && in.ID != "" && strings.TrimRight(old.BaseURL, "/") == in.BaseURL {
		// The stored key is only sent to the URL it was saved for.
		in.APIKey = keepKey(in.APIKey, old.APIKey)
		in.APIKeyEnv = old.APIKeyEnv
	} else if in.APIKey == maskedToken || strings.Contains(in.APIKey, "…") {
		in.APIKey = ""
	}
	models, err := p.discover(r.Context(), in)
	if err != nil {
		return nil, bad("%v", err)
	}
	return map[string]any{"models": models}, nil
}

// refreshModels replaces a saved provider's models with the live list.
func (p *Panel) refreshModels(r *http.Request) (any, error) {
	id := r.PathValue("id")
	pr, err := p.provider(id)
	if err != nil {
		return nil, err
	}
	models, err := p.discover(r.Context(), pr)
	if err != nil {
		return nil, bad("%v", err)
	}
	cfg, err := p.Store.Update(func(c *config.Config) error {
		for i := range c.Providers {
			if c.Providers[i].ID == id {
				c.Providers[i].Models = models
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	saved, _ := cfg.Provider(id)
	return viewProvider(saved), nil
}

// discover calls the provider's /models endpoint.
func (p *Panel) discover(ctx context.Context, pr config.Provider) ([]string, error) {
	base := strings.TrimRight(pr.BaseURL, "/")
	if pr.Type == config.TypeAnthropic {
		base = strings.TrimSuffix(base, "/v1") + "/v1"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if key := pr.Key(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("x-api-key", key)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range pr.Headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch models: HTTP %d: %.300s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct { // Ollama-style
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("fetch models: unexpected reply: %.200s", string(data))
	}
	seen := map[string]bool{}
	models := []string{}
	add := func(m string) {
		if m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	for _, m := range list.Data {
		add(m.ID)
	}
	for _, m := range list.Models {
		add(m.Name)
	}
	return models, nil
}

func (p *Panel) testProvider(r *http.Request) (any, error) {
	pr, err := p.provider(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Model string `json:"model"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if in.Model == "" && len(pr.Models) > 0 {
		in.Model = pr.Models[0]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	reply, took, err := p.Proxy.Test(ctx, pr, in.Model)
	res := map[string]any{"ok": err == nil, "latency_ms": took.Milliseconds(), "reply": reply, "error": ""}
	if err != nil {
		res["error"] = err.Error()
	}
	return res, nil
}

// ---- agents ----------------------------------------------------------------

type agentView struct {
	agents.Agent
	Route config.Route `json:"route"`
}

func (p *Panel) dir() agents.Dir {
	return agents.Dir{Path: p.Store.Get().Server.AgentsDir, TrashDir: filepath.Join(config.Home(), "trash")}
}

func routeOrDefault(rt config.Route) config.Route {
	if rt.SystemMode == "" {
		rt.SystemMode = config.SystemKeep
	}
	return rt
}

func (p *Panel) listAgents(*http.Request) (any, error) {
	list, err := p.dir().List()
	if err != nil {
		return nil, err
	}
	cfg := p.Store.Get()
	out := make([]agentView, 0, len(list))
	for _, a := range list {
		out = append(out, agentView{Agent: a, Route: routeOrDefault(cfg.Agents[a.Name])})
	}
	return out, nil
}

// cleanRoute drops settings that do nothing so the config file stays tidy.
func cleanRoute(rt config.Route) config.Route {
	if rt.Provider == "" {
		return config.Route{}
	}
	if rt.SystemMode == config.SystemKeep {
		rt.SystemMode = ""
		rt.SystemPrompt = ""
	}
	return rt
}

func (p *Panel) putAgent(r *http.Request) (any, error) {
	oldName := r.PathValue("name")
	var in agentView
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = oldName
	}
	if !config.ValidAgentName(in.Name) || !config.ValidAgentName(oldName) {
		return nil, bad("agent name may only use letters, digits, '.', '-' and '_'")
	}
	if strings.TrimSpace(in.Description) == "" {
		return nil, bad("description is required: Claude uses it to decide when to call the agent")
	}
	rt := cleanRoute(in.Route)
	// Check the route before touching any file.
	if rt.Provider != "" {
		if _, ok := p.Store.Get().Provider(rt.Provider); !ok {
			return nil, bad("unknown provider %q", rt.Provider)
		}
	}
	a := in.Agent
	a.Extra = nil // keep whatever the file already has
	d := p.dir()
	if _, err := os.Stat(filepath.Join(d.Path, oldName+".md")); err != nil {
		oldName = "" // new agent
		if _, err := os.Stat(filepath.Join(d.Path, a.Name+".md")); err == nil {
			return nil, bad("an agent named %q already exists", a.Name)
		}
	}
	saved, err := d.Save(oldName, a)
	if err != nil {
		return nil, bad("%v", err)
	}
	cfg, err := p.Store.Update(func(c *config.Config) error {
		if oldName != "" {
			delete(c.Agents, oldName)
		}
		if rt.Provider != "" {
			c.Agents[saved.Name] = rt
		} else {
			delete(c.Agents, saved.Name)
		}
		return nil
	})
	if err != nil {
		return nil, bad("agent file saved, but routing was not: %v", err)
	}
	reloaded, err := d.Get(saved.Name)
	if err != nil {
		reloaded = saved
	}
	return agentView{Agent: reloaded, Route: routeOrDefault(cfg.Agents[saved.Name])}, nil
}

func (p *Panel) deleteAgent(r *http.Request) (any, error) {
	name := r.PathValue("name")
	if !config.ValidAgentName(name) {
		return nil, bad("invalid agent name")
	}
	dst, err := p.dir().Trash(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, notFound("agent %q not found", name)
		}
		return nil, err
	}
	if _, err := p.Store.Update(func(c *config.Config) error { delete(c.Agents, name); return nil }); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "trashed_to": dst}, nil
}

// ---- rules & settings ------------------------------------------------------

func (p *Panel) putRules(r *http.Request) (any, error) {
	var in []config.Rule
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	for i := range in {
		in[i].Match = strings.TrimSpace(in[i].Match)
		if in[i].Match == "" {
			return nil, bad("rule %d: match pattern is empty", i+1)
		}
		in[i].Route = cleanRoute(in[i].Route)
	}
	cfg, err := p.Store.Update(func(c *config.Config) error { c.Rules = in; return nil })
	if err != nil {
		return nil, bad("%v", err)
	}
	return nonNil(cfg.Rules), nil
}

func (p *Panel) putSettings(r *http.Request) (any, error) {
	var in config.Server
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	var before config.Server
	cfg, err := p.Store.Update(func(c *config.Config) error {
		before = c.Server
		if in.PanelToken == maskedToken || in.PanelToken == "" {
			in.PanelToken = c.Server.PanelToken
		}
		for _, addr := range []string{in.ProxyListen, in.PanelListen} {
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return bad("listen address %q: use host:port", addr)
			}
		}
		lockFileOnly(&in, c.Server)
		c.Server = in
		return nil
	})
	if err != nil {
		return nil, err
	}
	s := cfg.Server
	restart := s.ProxyListen != before.ProxyListen || s.PanelListen != before.PanelListen || s.HeaderTimeout != before.HeaderTimeout
	return map[string]any{"settings": maskSettings(s), "restart_required": restart}, nil
}

// ---- raw config ------------------------------------------------------------

func (p *Panel) getRaw(*http.Request) (any, error) {
	cfg := p.Store.Get()
	for i := range cfg.Providers {
		cfg.Providers[i].APIKey = maskKey(cfg.Providers[i].APIKey)
	}
	cfg.Server = maskSettings(cfg.Server)
	data, err := config.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return map[string]string{"yaml": string(data)}, nil
}

func (p *Panel) putRaw(r *http.Request) (any, error) {
	var in struct {
		YAML string `json:"yaml"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	next, err := config.Parse([]byte(in.YAML))
	if err != nil {
		return nil, bad("%v", err)
	}
	_, err = p.Store.Update(func(c *config.Config) error {
		// Masked values mean "unchanged".
		for i, np := range next.Providers {
			if old, ok := c.Provider(np.ID); ok {
				next.Providers[i].APIKey = keepKey(np.APIKey, old.APIKey)
			} else if np.APIKey == maskedToken {
				next.Providers[i].APIKey = ""
			}
		}
		if next.Server.PanelToken == maskedToken {
			next.Server.PanelToken = c.Server.PanelToken
		}
		lockFileOnly(&next.Server, c.Server)
		*c = *next
		return nil
	})
	if err != nil {
		return nil, bad("%v", err)
	}
	return map[string]bool{"ok": true}, nil
}

// usageReport returns the savings analysis for the dashboard.
func (p *Panel) usageReport(*http.Request) (any, error) {
	if p.Usage == nil {
		return nil, notFound("usage tracking is not enabled")
	}
	srv := p.Store.Current().Server
	var plan *usage.Plan
	if pl, ok := usage.PlanByID(srv.Plan, srv.PlanPrice); ok {
		plan = &pl
	}
	return p.Usage.Build(time.Now(), plan), nil
}

// lockFileOnly keeps the settings that decide where the subscription token,
// the log and agent files go. Changing them from a browser (or from a
// prompt-injected tool calling this API) would leak the token or write
// arbitrary files, so they can only be changed by editing config.yaml.
func lockFileOnly(next *config.Server, cur config.Server) {
	next.Upstream = cur.Upstream
	next.LogFile = cur.LogFile
	next.AgentsDir = cur.AgentsDir
}
