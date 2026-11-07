package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenCreatesDefaultAndUpdateValidates(t *testing.T) {
	t.Setenv("CLAUDE_HYBRID_HOME", t.TempDir())
	path := filepath.Join(Home(), "config.yaml")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("config must be 0600, got %v", fi.Mode().Perm())
	}
	if s.Get().Server.ProxyListen != "127.0.0.1:3457" {
		t.Fatal("defaults missing")
	}
	_, err = s.Update(func(c *Config) error {
		c.Agents["x"] = Route{Provider: "nope"}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("want validation error, got %v", err)
	}
	if len(s.Get().Agents) != 0 {
		t.Fatal("failed update must not change the live config")
	}
	_, err = s.Update(func(c *Config) error {
		c.Providers = append(c.Providers, Provider{ID: "gpt", BaseURL: "https://x/v1", Enabled: true})
		c.Rules = []Rule{{Match: "claude-haiku-*", Enabled: true, Route: Route{Provider: "gpt"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(path)
	c := s2.Get()
	if p, _ := c.Provider("gpt"); p.Type != TypeOpenAI {
		t.Fatal("type should default to openai")
	}
	if !c.Rules[0].Matches("claude-haiku-5-5") || c.Rules[0].Matches("claude-opus-5") {
		t.Fatal("rule matching")
	}
}

func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(c.Server.AgentsDir, "~") || len(c.Agents) != 2 || len(c.Rules) != 1 {
		t.Fatalf("parsed %+v", c.Server)
	}
}
