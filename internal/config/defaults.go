package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Default returns a config with every field set to its default.
func Default() *Config {
	c := &Config{Version: CurrentVersion}
	c.setDefaults()
	return c
}

// setDefaults fills in every empty field, then normalizes.
func (c *Config) setDefaults() {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	s := &c.Server
	if s.ProxyListen == "" {
		s.ProxyListen = "127.0.0.1:3457"
	}
	if s.PanelListen == "" {
		s.PanelListen = "127.0.0.1:3458"
	}
	if s.Upstream == "" {
		s.Upstream = "https://api.anthropic.com"
	}
	if s.HeaderTimeout <= 0 {
		s.HeaderTimeout = 300
	}
	if s.AgentsDir == "" {
		s.AgentsDir = defaultAgentsDir()
	}
	if s.LogFile == "" {
		s.LogFile = filepath.Join(Home(), "proxy.log")
	}
	if c.Agents == nil {
		c.Agents = map[string]Route{}
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Type == "" {
			p.Type = TypeOpenAI
		}
		if p.Name == "" {
			p.Name = p.ID
		}
	}
	c.normalize()
}

func defaultAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "agents")
}

// normalize fixes harmless leftovers instead of refusing to load: "~"
// in paths is expanded, rules without a pattern never match so they are
// dropped.
func (c *Config) normalize() {
	c.Server.AgentsDir, c.Server.LogFile = expandHome(c.Server.AgentsDir), expandHome(c.Server.LogFile)
	rules := c.Rules[:0]
	for _, r := range c.Rules {
		r.Match = strings.TrimSpace(r.Match)
		if r.Match != "" {
			rules = append(rules, r)
		}
	}
	c.Rules = rules
	if len(c.Rules) == 0 {
		c.Rules = nil
	}
}
