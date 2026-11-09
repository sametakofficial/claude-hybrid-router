package panel

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/agents"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/marker"
)

type agentView struct {
	agents.Agent
	Route config.Route `json:"route"`
	// Missing: config has a route for this name but there is no agent file.
	Missing bool `json:"missing"`
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

// listAgents returns every agent file, plus routes in config.yaml whose
// agent file is missing (so they can be recreated or removed).
func (p *Panel) listAgents(*http.Request) (any, error) {
	list, err := p.dir().List()
	if err != nil {
		return nil, err
	}
	cfg := p.Store.Current()
	out := make([]agentView, 0, len(list)+len(cfg.Agents))
	seen := map[string]bool{}
	for _, a := range list {
		seen[a.Name] = true
		out = append(out, agentView{Agent: a, Route: routeOrDefault(cfg.Agents[a.Name])})
	}
	for name, rt := range cfg.Agents {
		if !seen[name] {
			out = append(out, agentView{Agent: agents.Agent{Name: name}, Route: routeOrDefault(rt), Missing: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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

func (p *Panel) checkRoute(rt config.Route) error {
	if rt.Provider != "" {
		if _, ok := p.Store.Current().Provider(rt.Provider); !ok {
			return bad("unknown provider %q", rt.Provider)
		}
	}
	return nil
}

// putAgent creates, edits, renames or recreates an agent file and sets its
// route from the body. {name} in the path is the agent's current name.
func (p *Panel) putAgent(r *http.Request) (any, error) {
	pathName := r.PathValue("name")
	var in agentView
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = pathName
	}
	if !marker.ValidName(in.Name) || !marker.ValidName(pathName) {
		return nil, bad("agent name may only use letters, digits, '.', '-' and '_'")
	}
	if strings.TrimSpace(in.Description) == "" {
		return nil, bad("description is required: Claude uses it to decide when to call the agent")
	}
	rt := cleanRoute(in.Route)
	if err := p.checkRoute(rt); err != nil { // before touching any file
		return nil, err
	}
	a := in.Agent
	a.Extra = nil // keep whatever the file already has
	d := p.dir()
	oldName := pathName
	if !d.Exists(oldName) {
		oldName = "" // new agent, or recreating a missing file
		if d.Exists(a.Name) {
			return nil, bad("an agent named %q already exists", a.Name)
		}
	}
	saved, err := d.Save(oldName, a)
	if err != nil {
		return nil, bad("%v", err)
	}
	cfg, err := p.Store.Update(func(c *config.Config) error {
		if pathName != saved.Name {
			delete(c.Agents, pathName)
		}
		c.SetAgentRoute(saved.Name, rt)
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

// putAgentRoute changes only the route in config.yaml; the agent file is
// not touched (and need not exist).
func (p *Panel) putAgentRoute(r *http.Request) (any, error) {
	name := r.PathValue("name")
	if !marker.ValidName(name) {
		return nil, bad("invalid agent name")
	}
	var in config.Route
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	rt := cleanRoute(in)
	if err := p.checkRoute(rt); err != nil {
		return nil, err
	}
	cfg, err := p.Store.Update(func(c *config.Config) error { c.SetAgentRoute(name, rt); return nil })
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "route": routeOrDefault(cfg.Agents[name])}, nil
}

// deleteAgent moves the file to the trash and removes the route. For a
// missing agent (route only) it just removes the route.
func (p *Panel) deleteAgent(r *http.Request) (any, error) {
	name := r.PathValue("name")
	if !marker.ValidName(name) {
		return nil, bad("invalid agent name")
	}
	dst, err := p.dir().Trash(name)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if os.IsNotExist(err) {
		if _, ok := p.Store.Current().Agents[name]; !ok {
			return nil, notFound("agent %q not found", name)
		}
	}
	if _, err := p.Store.Update(func(c *config.Config) error { delete(c.Agents, name); return nil }); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "trashed_to": dst}, nil
}
