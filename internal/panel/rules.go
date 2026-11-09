package panel

import (
	"net/http"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

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
