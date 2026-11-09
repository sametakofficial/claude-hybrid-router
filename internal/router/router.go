// Package router decides where one Messages request goes. It has no HTTP
// code: the proxy feeds it the body and acts on the Decision.
package router

import (
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/marker"
)

// Via says what picked the route.
type Via int

const (
	ViaDefault Via = iota // nothing matched: Anthropic subscription
	ViaAgent              // agent marker in the system prompt
	ViaRule               // model rule
	ViaLegacy             // v1 URL marker
)

// Decision is where one request goes.
type Decision struct {
	Via       Via
	Agent     string // ViaAgent: the agent name from the marker
	Rule      int    // ViaRule: 1-based rule number
	Route     config.Route
	LegacyURL string            // ViaLegacy: forward as-is to this Anthropic-compatible URL
	Req       anthropic.Request // parsed body with markers removed; nil if the body is not JSON
	Body      []byte            // body to send to Anthropic: the original bytes unless a marker was removed
}

// Source names what picked the route, for logs: "agent:NAME", "rule:N", "legacy" or "".
func (d Decision) Source() string {
	switch d.Via {
	case ViaAgent:
		return "agent:" + d.Agent
	case ViaRule:
		return "rule:" + strconv.Itoa(d.Rule)
	case ViaLegacy:
		return "legacy"
	}
	return ""
}

// Subscription reports whether the request goes to Anthropic unchanged
// (apart from a removed marker) with the client's own credentials.
func (d Decision) Subscription() bool { return d.Via != ViaLegacy && d.Route.IsAnthropic() }

// Decide picks the route for a request body. A body that is not JSON is
// returned as a subscription decision together with the parse error.
//
// Order: agent marker, then a loopback v1 URL marker, then the first
// enabled rule whose pattern matches the model, else Anthropic. An agent
// marker without a configured route also means Anthropic (no rule applies).
func Decide(cfg *config.Config, body []byte) (Decision, error) {
	d := Decision{Body: body}
	req, err := anthropic.ParseRequest(body)
	if err != nil {
		return d, err
	}
	d.Req = req
	agent, legacyURL, found := findMarkers(req)
	if found {
		out, err := json.Marshal(req)
		if err != nil {
			return d, err
		}
		d.Body = out
	}
	switch {
	case agent != "":
		d.Via, d.Agent = ViaAgent, agent
		d.Route = cfg.Agents[agent] // missing entry = Anthropic
		return d, nil
	case legacyURL != "" && isLoopbackURL(legacyURL):
		// v1 markers carry their own URL. Only local URLs are honoured: a
		// project agent file from a cloned repo must not be able to send the
		// conversation to an arbitrary server.
		d.Via, d.LegacyURL = ViaLegacy, legacyURL
		return d, nil
	}
	model := req.Model()
	for i, rule := range cfg.Rules {
		if rule.Matches(model) {
			d.Via, d.Rule, d.Route = ViaRule, i+1, rule.Route
			return d, nil
		}
	}
	return d, nil
}

// findMarkers looks for routing markers in the system field and removes
// them. Only the system field is scanned: a marker inside a user message or
// tool result (for example after reading an agent file) must not reroute
// the main conversation. System text left empty is dropped, since the API
// rejects empty text blocks.
func findMarkers(r anthropic.Request) (agent, legacyURL string, found bool) {
	r.EditSystem(func(t string) string {
		hit := false
		if m := marker.AgentRE.FindStringSubmatch(t); m != nil {
			if agent == "" {
				agent = m[1]
			}
			hit = true
			t = marker.AgentRE.ReplaceAllString(t, "")
		}
		if m := marker.LegacyRE.FindStringSubmatch(t); m != nil {
			if legacyURL == "" {
				legacyURL = m[1]
			}
			hit = true
			t = marker.LegacyRE.ReplaceAllString(t, "")
		}
		if hit {
			found = true
			t = strings.TrimSpace(t)
		}
		return t
	})
	if found {
		dropEmptySystem(r)
	}
	return
}

// dropEmptySystem removes empty text blocks from system, and system itself
// when nothing is left.
func dropEmptySystem(r anthropic.Request) {
	switch s := r["system"].(type) {
	case string:
		if s == "" {
			delete(r, "system")
		}
	case []any:
		kept := s[:0]
		for _, b := range s {
			if m, ok := b.(map[string]any); ok && m["type"] == "text" {
				if t, _ := m["text"].(string); t == "" {
					continue
				}
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			delete(r, "system")
		} else {
			r["system"] = kept
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
