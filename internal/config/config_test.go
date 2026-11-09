package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestValidateRejectsBadAddressesAndURLs(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"proxy listen": func(c *Config) { c.Server.ProxyListen = "3457" },
		"panel port":   func(c *Config) { c.Server.PanelListen = "127.0.0.1:99999" },
		"upstream":     func(c *Config) { c.Server.Upstream = "api.anthropic.com" },
		"base url":     func(c *Config) { c.Providers = []Provider{{ID: "x", Type: TypeOpenAI, BaseURL: "https://"}} },
		"type":         func(c *Config) { c.Providers = []Provider{{ID: "x", Type: "gemini", BaseURL: "https://x"}} },
		"empty rule":   func(c *Config) { c.Rules = []Rule{{Match: " "}} },
	} {
		c := Default()
		mut(c)
		err := c.Validate()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: want ValidationError, got %v", name, err)
		}
	}
}

func TestLoadDropsEmptyRules(t *testing.T) {
	c, err := Parse([]byte("version: 2\nrules:\n  - match: \"\"\n    enabled: true\n  - match: claude-*\n    enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != 1 || c.Rules[0].Match != "claude-*" {
		t.Fatalf("rules: %+v", c.Rules)
	}
}

func TestUpdateKeepsHandEditMadeJustBefore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Hand edit: add a provider. Before the fix, a panel save within the
	// watcher's 2s window wrote the old in-memory config over it.
	data, _ := os.ReadFile(path)
	edited := strings.Replace(string(data), "providers: []", "providers:\n  - id: hand\n    base_url: https://h/v1\n    enabled: true", 1)
	if edited == string(data) {
		t.Fatalf("test setup: no providers line in\n%s", data)
	}
	os.WriteFile(path, []byte(edited), 0o600)
	if _, err := s.Update(func(c *Config) error { c.Server.Plan = "pro"; return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(path)
	if _, ok := c.Provider("hand"); !ok || c.Server.Plan != "pro" {
		t.Fatalf("hand edit or update lost: %+v", c)
	}
	// An invalid hand edit is not overwritten.
	os.WriteFile(path, []byte("version: 2\nserver: [broken"), 0o600)
	if _, err := s.Update(func(c *Config) error { return nil }); err == nil {
		t.Fatal("update over an invalid hand edit must fail")
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "broken") {
		t.Fatal("invalid hand edit was overwritten")
	}
}

func TestWatchReloadsOutsideEditsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	s, _ := Open(path)
	logs := make(chan string, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx, 5*time.Millisecond, func(f string, a ...any) { logs <- fmt.Sprintf(f, a...) })
	s.Update(func(c *Config) error { c.Server.Plan = "max5"; return nil })
	select {
	case l := <-logs:
		t.Fatalf("own write must not trigger a reload: %s", l)
	case <-time.After(50 * time.Millisecond):
	}
	os.WriteFile(path, []byte("version: 2\nserver:\n  plan: max20\n"), 0o600)
	select {
	case l := <-logs:
		if !strings.Contains(l, "reloaded") || s.Current().Server.Plan != "max20" {
			t.Fatalf("log %q plan %q", l, s.Current().Server.Plan)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outside edit not reloaded")
	}
}

func TestLoadDoesNotCreateTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if c, err := Load(path); err != nil || c.Server.ProxyListen == "" {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Load must not create config.yaml")
	}
}

func TestParseMigratesAndRejects(t *testing.T) {
	c, err := Parse([]byte("server:\n  plan: pro\nproviders:\n  - id: a\n    base_url: https://a/v1\n"))
	if err != nil || c.Version != CurrentVersion || c.Server.Plan != "pro" || len(c.Providers) != 1 {
		t.Fatalf("version 0 doc: %+v %v", c, err)
	}
	for name, doc := range map[string]string{
		"empty":    "",
		"comments": "# nothing here\n",
		"newer":    "version: 99\n",
		"v1":       "version: 1\n",
		"list":     "- a\n",
		"badver":   "version: two\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestOpenRefusesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, nil, 0o600)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty config must not load as defaults: %v", err)
	}
}
