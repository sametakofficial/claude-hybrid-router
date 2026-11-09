package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func (p *Panel) listProviders(*http.Request) (any, error) {
	cfg := p.Store.Current()
	out := make([]providerView, 0, len(cfg.Providers))
	for _, pr := range cfg.Providers {
		out = append(out, viewProvider(pr))
	}
	return out, nil
}

// providerInput is the body of PUT /api/providers/{id}.
type providerInput struct {
	config.Provider
	ClearAPIKey bool `json:"clear_api_key"` // remove the stored key
}

func (p *Panel) putProvider(r *http.Request) (any, error) {
	id := r.PathValue("id")
	var body providerInput
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	in := body.Provider
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
	create := r.URL.Query().Get("create") == "1"
	resolve := func(c *config.Config) (config.Provider, int) {
		for i := range c.Providers {
			if c.Providers[i].ID == id {
				pr, _ := unmaskProvider(in, &c.Providers[i], body.ClearAPIKey)
				return pr, i
			}
		}
		pr, _ := unmaskProvider(in, nil, body.ClearAPIKey)
		return pr, -1
	}
	if len(in.Models) == 0 {
		if probe, i := resolve(p.Store.Current()); i < 0 || !create {
			if models, err := p.discover(r.Context(), probe); err == nil {
				in.Models = models
			}
		}
	}
	cfg, err := p.Store.Update(func(c *config.Config) error {
		pr, i := resolve(c)
		switch {
		case i >= 0 && create:
			return bad("a provider with id %q already exists", id)
		case i >= 0:
			c.Providers[i] = pr
		default:
			c.Providers = append(c.Providers, pr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	saved, _ := cfg.Provider(id)
	return viewProvider(saved), nil
}

// putProviderEnabled switches one provider on or off without touching
// anything else (the UI switch must not resend a stale provider).
func (p *Panel) putProviderEnabled(r *http.Request) (any, error) {
	id := r.PathValue("id")
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if in.Enabled == nil {
		return nil, bad("body must be {\"enabled\": true|false}")
	}
	cfg, err := p.Store.Update(func(c *config.Config) error {
		for i := range c.Providers {
			if c.Providers[i].ID == id {
				c.Providers[i].Enabled = *in.Enabled
				return nil
			}
		}
		return notFound("provider %q not found", id)
	})
	if err != nil {
		return nil, err
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
	pr, ok := p.Store.Current().Provider(id)
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
	if old, ok := p.Store.Current().Provider(in.ID); ok && in.ID != "" && strings.TrimRight(old.BaseURL, "/") == in.BaseURL {
		// Stored secrets are only sent to the URL they were saved for.
		in, _ = unmaskProvider(in, &old, false)
		if in.APIKeyEnv == "" {
			in.APIKeyEnv = old.APIKeyEnv
		}
	} else {
		in, _ = unmaskProvider(in, nil, false)
	}
	models, err := p.discover(r.Context(), in)
	if err != nil {
		return nil, err
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
		return nil, err
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
