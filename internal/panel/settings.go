package panel

import (
	"net/http"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func (p *Panel) putSettings(r *http.Request) (any, error) {
	var in config.Server
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	var before config.Server
	cfg, err := p.Store.Update(func(c *config.Config) error {
		before = c.Server
		in.PanelToken = unmaskToken(in.PanelToken, c.Server.PanelToken)
		if err := checkToken(in.PanelToken, c.Server.PanelToken); err != nil {
			return err
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

// minTokenLen is enforced only when the token is changed from the panel, so
// an existing config with a shorter token still loads.
const minTokenLen = 16

func checkToken(next, cur string) error {
	if next != "" && next != cur && len(next) < minTokenLen {
		return bad("panel token must be at least %d characters", minTokenLen)
	}
	return nil
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
