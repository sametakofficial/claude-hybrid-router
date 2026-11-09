package panel

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/provider"
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

// discover reads the provider's model list through its adapter.
func (p *Panel) discover(ctx context.Context, pr config.Provider) ([]string, error) {
	ad, err := provider.New(pr, p.client())
	if err != nil {
		return nil, err
	}
	return ad.ListModels(ctx)
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
	ad, err := provider.New(pr, p.client())
	if err != nil {
		return nil, bad("%v", err)
	}
	reply, took, err := provider.Test(ctx, ad, in.Model)
	res := map[string]any{"ok": err == nil, "latency_ms": took.Milliseconds(), "reply": reply, "error": ""}
	if err != nil {
		res["error"] = err.Error()
	}
	return res, nil
}
