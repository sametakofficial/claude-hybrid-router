package panel

import (
	"net"
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

// lockFileOnly keeps the settings that decide where the subscription token,
// the log and agent files go. Changing them from a browser (or from a
// prompt-injected tool calling this API) would leak the token or write
// arbitrary files, so they can only be changed by editing config.yaml.
func lockFileOnly(next *config.Server, cur config.Server) {
	next.Upstream = cur.Upstream
	next.LogFile = cur.LogFile
	next.AgentsDir = cur.AgentsDir
}
