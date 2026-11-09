package config

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	idRE        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	agentNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// ValidAgentName reports whether s is safe to use as an agent file name.
func ValidAgentName(s string) bool { return agentNameRE.MatchString(s) && !strings.Contains(s, "..") }

// ValidationError is a mistake in the config itself (as opposed to an I/O
// failure while loading or saving it).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{fmt.Sprintf(format, a...)} }

func checkListen(name, addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return invalid("%s %q: use host:port", name, addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return invalid("%s %q: port must be 1-65535", name, addr)
	}
	return nil
}

func checkURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("%s must be an http:// or https:// URL, got %q", name, raw)
	}
	return nil
}

// Validate checks the config for mistakes that would break routing. Errors
// are *ValidationError.
func (c *Config) Validate() error {
	s := c.Server
	if err := checkListen("server.proxy_listen", s.ProxyListen); err != nil {
		return err
	}
	if err := checkListen("server.panel_listen", s.PanelListen); err != nil {
		return err
	}
	if err := checkURL("server.upstream", s.Upstream); err != nil {
		return err
	}
	switch s.Plan {
	case "", "pro", "max5", "max20", "custom":
	default:
		return invalid("server.plan must be pro, max5, max20 or custom")
	}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if !idRE.MatchString(p.ID) {
			return invalid("provider id %q: use lowercase letters, digits, - or _", p.ID)
		}
		if seen[p.ID] {
			return invalid("duplicate provider id %q", p.ID)
		}
		seen[p.ID] = true
		if !slices.Contains(ProviderTypes, p.Type) {
			return invalid("provider %q: type must be one of %s", p.ID, strings.Join(ProviderTypes, ", "))
		}
		if err := checkURL(fmt.Sprintf("provider %q: base_url", p.ID), p.BaseURL); err != nil {
			return err
		}
	}
	check := func(where string, r Route) error {
		if r.Provider != "" && !seen[r.Provider] {
			return invalid("%s: unknown provider %q", where, r.Provider)
		}
		switch r.SystemMode {
		case "", SystemKeep, SystemReplace, SystemAppend:
		default:
			return invalid("%s: system_mode must be keep, replace or append", where)
		}
		switch r.ReasoningEffort {
		case "", "minimal", "low", "medium", "high":
		default:
			return invalid("%s: reasoning_effort must be minimal, low, medium or high", where)
		}
		if r.MaxTokens < 0 {
			return invalid("%s: max_tokens must not be negative", where)
		}
		return nil
	}
	for name, r := range c.Agents {
		if !ValidAgentName(name) {
			return invalid("agent name %q is not valid", name)
		}
		if err := check("agent "+name, r); err != nil {
			return err
		}
	}
	for i, r := range c.Rules {
		if strings.TrimSpace(r.Match) == "" {
			return invalid("rule %d: match pattern is empty", i+1)
		}
		if _, err := path.Match(r.Match, ""); err != nil {
			return invalid("rule %d: bad pattern %q", i+1, r.Match)
		}
		if err := check(fmt.Sprintf("rule %d", i+1), r.Route); err != nil {
			return err
		}
	}
	return nil
}
