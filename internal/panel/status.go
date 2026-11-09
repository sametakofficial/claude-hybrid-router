package panel

import (
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
)

var lookPath = exec.LookPath // swapped in tests

func (p *Panel) status(*http.Request) (any, error) {
	cfg := p.Store.Current()
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
