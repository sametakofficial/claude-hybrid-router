package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/agents"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/marker"
)

type agentView struct {
	agents.Agent
	Route config.Route `json:"route"`
}

func (p *Panel) dir() agents.Dir {
	return agents.Dir{Path: p.Store.Current().Server.AgentsDir, TrashDir: config.TrashDir()}
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
	cfg := p.Store.Current()
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
	if !marker.ValidName(in.Name) || !marker.ValidName(oldName) {
		return nil, bad("agent name may only use letters, digits, '.', '-' and '_'")
	}
	if strings.TrimSpace(in.Description) == "" {
		return nil, bad("description is required: Claude uses it to decide when to call the agent")
	}
	rt := cleanRoute(in.Route)
	// Check the route before touching any file.
	if rt.Provider != "" {
		if _, ok := p.Store.Current().Provider(rt.Provider); !ok {
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
	if !marker.ValidName(name) {
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
