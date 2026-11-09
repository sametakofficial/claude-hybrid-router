package panel

import (
	"net/http"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

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
