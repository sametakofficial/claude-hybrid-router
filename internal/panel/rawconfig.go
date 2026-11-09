package panel

import (
	"net/http"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func (p *Panel) getRaw(*http.Request) (any, error) {
	cfg := p.Store.Get()
	for i := range cfg.Providers {
		cfg.Providers[i].APIKey = maskKey(cfg.Providers[i].APIKey)
		if len(cfg.Providers[i].Headers) > 0 {
			cfg.Providers[i].Headers = maskHeaders(cfg.Providers[i].Headers)
		}
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
		return nil, err
	}
	_, err = p.Store.Update(func(c *config.Config) error {
		// Masked values mean "unchanged". A provider that is new (or was
		// renamed) has no stored secret to fall back on: refuse instead of
		// saving the mask text as its key.
		for i, np := range next.Providers {
			var stored *config.Provider
			if old, ok := c.Provider(np.ID); ok {
				stored = &old
			}
			pr, dropped := unmaskProvider(np, stored, false)
			if dropped {
				return bad("provider %q: api_key or a header holds a masked value but there is no stored value for this id; paste the real value (or use api_key_env)", np.ID)
			}
			next.Providers[i] = pr
		}
		next.Server.PanelToken = unmaskToken(next.Server.PanelToken, c.Server.PanelToken)
		if err := checkToken(next.Server.PanelToken, c.Server.PanelToken); err != nil {
			return err
		}
		lockFileOnly(&next.Server, c.Server)
		*c = *next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}
