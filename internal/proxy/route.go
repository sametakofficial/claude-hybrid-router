package proxy

import (
	"encoding/json"
	"fmt"
	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"net"
	"net/url"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/marker"
)

// Decision is where one request goes.
type Decision struct {
	Source    string // "agent:NAME", "rule:N", "legacy" or ""
	Route     config.Route
	LegacyURL string // set for v1 markers: forward as-is to this Anthropic-compatible URL
	Body      []byte // body to send (marker removed)
}

// findMarkers looks for routing markers in the system field and removes them.
// Only the system field is scanned: a marker inside a user message or tool
// result (for example after reading an agent file) must not reroute the
// main conversation.
func findMarkers(r anthropic.Request) (agent, legacyURL string, found bool) {
	r.EditSystem(func(t string) string {
		if m := marker.AgentRE.FindStringSubmatch(t); m != nil {
			if agent == "" {
				agent = m[1]
			}
			found = true
			t = marker.AgentRE.ReplaceAllString(t, "")
		}
		if m := marker.LegacyRE.FindStringSubmatch(t); m != nil {
			if legacyURL == "" {
				legacyURL = m[1]
			}
			found = true
			t = marker.LegacyRE.ReplaceAllString(t, "")
		}
		if found {
			t = strings.TrimSpace(t)
		}
		return t
	})
	return
}

// decide picks the route for a request.
func decide(cfg *config.Config, body []byte) (Decision, anthropic.Request, error) {
	req, err := anthropic.ParseRequest(body)
	if err != nil {
		return Decision{Body: body}, nil, err
	}
	d := Decision{Body: body}
	agent, legacyURL, found := findMarkers(req)
	if found {
		out, err := json.Marshal(req)
		if err != nil {
			return d, req, err
		}
		d.Body = out
	}
	switch {
	case agent != "":
		d.Source = "agent:" + agent
		d.Route = cfg.Agents[agent] // missing entry = Anthropic
		return d, req, nil
	case legacyURL != "" && isLoopbackURL(legacyURL):
		// v1 markers carry their own URL. Only local URLs are honoured: a
		// project agent file from a cloned repo must not be able to send the
		// conversation to an arbitrary server.
		d.Source = "legacy"
		d.LegacyURL = legacyURL
		return d, req, nil
	}
	model := req.Model()
	for i, rule := range cfg.Rules {
		if rule.Matches(model) {
			d.Source = fmt.Sprintf("rule:%d", i+1)
			d.Route = rule.Route
			return d, req, nil
		}
	}
	return d, req, nil
}

// applyRoute adjusts the request for a non-Anthropic target: system prompt
// mode and the overrides that are shared by every provider type.
func applyRoute(req anthropic.Request, rt config.Route) {
	switch rt.SystemMode {
	case config.SystemReplace:
		req["system"] = []any{map[string]any{"type": "text", "text": rt.SystemPrompt}}
	case config.SystemAppend:
		if rt.SystemPrompt == "" {
			break
		}
		block := map[string]any{"type": "text", "text": rt.SystemPrompt}
		switch s := req["system"].(type) {
		case string:
			req["system"] = []any{map[string]any{"type": "text", "text": s}, block}
		case []any:
			req["system"] = append(s, block)
		default:
			req["system"] = []any{block}
		}
	}
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
