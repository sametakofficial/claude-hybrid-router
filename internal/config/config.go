// Package config holds the claude-hybrid configuration schema and a
// thread-safe store that loads and saves it atomically.
package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Provider types.
const (
	TypeOpenAI    = "openai"    // OpenAI-compatible /chat/completions (translated)
	TypeAnthropic = "anthropic" // Anthropic-compatible /v1/messages (passed through)
)

// System prompt modes for a route.
const (
	SystemKeep    = "keep"
	SystemReplace = "replace"
	SystemAppend  = "append"
)

// Config is the on-disk configuration (~/.claude-hybrid/config.yaml).
type Config struct {
	Version   int              `yaml:"version" json:"version"`
	Server    Server           `yaml:"server" json:"server"`
	Providers []Provider       `yaml:"providers" json:"providers"`
	Agents    map[string]Route `yaml:"agents" json:"agents"`
	Rules     []Rule           `yaml:"rules" json:"rules"`
}

// Server holds listener and behaviour settings.
type Server struct {
	ProxyListen   string `yaml:"proxy_listen" json:"proxy_listen"`
	PanelListen   string `yaml:"panel_listen" json:"panel_listen"`
	PanelToken    string `yaml:"panel_token,omitempty" json:"panel_token"`
	Upstream      string `yaml:"upstream" json:"upstream"`
	HeaderTimeout int    `yaml:"header_timeout" json:"header_timeout"` // seconds
	AgentsDir     string `yaml:"agents_dir" json:"agents_dir"`
	LogFile       string `yaml:"log_file" json:"log_file"`
	// Claude subscription plan, used for the savings estimate on the dashboard:
	// "pro", "max5", "max20", "custom" or "".
	Plan      string  `yaml:"plan,omitempty" json:"plan"`
	PlanPrice float64 `yaml:"plan_price,omitempty" json:"plan_price"` // USD/month for "custom"
}

// Provider is a model backend that routed requests can be sent to.
type Provider struct {
	ID             string            `yaml:"id" json:"id"`
	Name           string            `yaml:"name,omitempty" json:"name"`
	Type           string            `yaml:"type" json:"type"`
	BaseURL        string            `yaml:"base_url" json:"base_url"`
	APIKey         string            `yaml:"api_key,omitempty" json:"api_key"`
	APIKeyEnv      string            `yaml:"api_key_env,omitempty" json:"api_key_env"`
	Headers        map[string]string `yaml:"headers,omitempty" json:"headers"`
	Models         []string          `yaml:"models,omitempty" json:"models"`
	MaxTokensField string            `yaml:"max_tokens_field,omitempty" json:"max_tokens_field"`
	Enabled        bool              `yaml:"enabled" json:"enabled"`
}

// Key returns the API key, preferring the literal value over the env var.
func (p Provider) Key() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv != "" {
		return os.Getenv(p.APIKeyEnv)
	}
	return ""
}

// Route says where a request goes and how it is adjusted on the way.
// An empty Provider means "Anthropic subscription" (plain pass-through).
type Route struct {
	Provider        string   `yaml:"provider,omitempty" json:"provider"`
	Model           string   `yaml:"model,omitempty" json:"model"`
	MaxTokens       int      `yaml:"max_tokens,omitempty" json:"max_tokens"`
	Temperature     *float64 `yaml:"temperature,omitempty" json:"temperature"`
	ReasoningEffort string   `yaml:"reasoning_effort,omitempty" json:"reasoning_effort"`
	SystemMode      string   `yaml:"system_mode,omitempty" json:"system_mode"`
	SystemPrompt    string   `yaml:"system_prompt,omitempty" json:"system_prompt"`
}

// IsAnthropic reports whether the route is a plain subscription pass-through.
func (r Route) IsAnthropic() bool { return r.Provider == "" }

// Rule routes non-agent requests by matching the request's model name.
type Rule struct {
	Match   string `yaml:"match" json:"match"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Note    string `yaml:"note,omitempty" json:"note"`
	Route   Route  `yaml:"route" json:"route"`
}

// Matches reports whether the rule applies to the given request model.
func (r Rule) Matches(model string) bool {
	if !r.Enabled || r.Match == "" {
		return false
	}
	ok, err := path.Match(r.Match, model)
	return err == nil && ok
}

var (
	idRE        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	agentNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// ValidAgentName reports whether s is safe to use as an agent file name.
func ValidAgentName(s string) bool { return agentNameRE.MatchString(s) && !strings.Contains(s, "..") }

// Home returns the claude-hybrid state directory (~/.claude-hybrid, or $CLAUDE_HYBRID_HOME).
func Home() string {
	if h := os.Getenv("CLAUDE_HYBRID_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude-hybrid"
	}
	return filepath.Join(home, ".claude-hybrid")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Default returns a config with every field set to its default.
func Default() *Config {
	c := &Config{Version: 2}
	c.applyDefaults()
	return c
}

func (c *Config) applyDefaults() {
	if c.Version == 0 {
		c.Version = 2
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
		if home, err := os.UserHomeDir(); err == nil {
			s.AgentsDir = filepath.Join(home, ".claude", "agents")
		}
	}
	if s.LogFile == "" {
		s.LogFile = filepath.Join(Home(), "proxy.log")
	}
	s.AgentsDir, s.LogFile = expandHome(s.AgentsDir), expandHome(s.LogFile)
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
}

// Validate checks the config for mistakes that would break routing.
func (c *Config) Validate() error {
	switch c.Server.Plan {
	case "", "pro", "max5", "max20", "custom":
	default:
		return fmt.Errorf("server.plan must be pro, max5, max20 or custom")
	}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if !idRE.MatchString(p.ID) {
			return fmt.Errorf("provider id %q: use lowercase letters, digits, - or _", p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		seen[p.ID] = true
		if p.Type != TypeOpenAI && p.Type != TypeAnthropic {
			return fmt.Errorf("provider %q: type must be %q or %q", p.ID, TypeOpenAI, TypeAnthropic)
		}
		if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
			return fmt.Errorf("provider %q: base_url must start with http:// or https://", p.ID)
		}
	}
	check := func(where string, r Route) error {
		if r.Provider != "" && !seen[r.Provider] {
			return fmt.Errorf("%s: unknown provider %q", where, r.Provider)
		}
		switch r.SystemMode {
		case "", SystemKeep, SystemReplace, SystemAppend:
		default:
			return fmt.Errorf("%s: system_mode must be keep, replace or append", where)
		}
		switch r.ReasoningEffort {
		case "", "minimal", "low", "medium", "high":
		default:
			return fmt.Errorf("%s: reasoning_effort must be minimal, low, medium or high", where)
		}
		return nil
	}
	for name, r := range c.Agents {
		if !ValidAgentName(name) {
			return fmt.Errorf("agent name %q is not valid", name)
		}
		if err := check("agent "+name, r); err != nil {
			return err
		}
	}
	for i, r := range c.Rules {
		if _, err := path.Match(r.Match, ""); err != nil {
			return fmt.Errorf("rule %d: bad pattern %q", i+1, r.Match)
		}
		if err := check(fmt.Sprintf("rule %d", i+1), r.Route); err != nil {
			return err
		}
	}
	return nil
}

// Provider returns the provider with the given id.
func (c *Config) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Clone returns a deep copy so callers can mutate it freely.
func (c *Config) Clone() *Config {
	out, _ := yaml.Marshal(c)
	var n Config
	_ = yaml.Unmarshal(out, &n)
	n.applyDefaults()
	return &n
}

// Parse decodes YAML into a validated config.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Version != 0 && c.Version != 2 {
		return nil, fmt.Errorf("config version %d is not supported (expected 2)", c.Version)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Marshal encodes the config as YAML.
func Marshal(c *Config) ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	header := "# claude-hybrid config. Edit here or in the web panel.\n"
	return append([]byte(header), data...), nil
}

// Store keeps the live config in memory and persists every change.
type Store struct {
	path string
	mu   sync.RWMutex
	cfg  *Config
}

// Open loads the config at path, creating a default one if it is missing.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.Reload(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		s.cfg = Default()
		if err := s.write(s.cfg); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Path returns the config file path.
func (s *Store) Path() string { return s.path }

// Get returns a copy of the current config.
func (s *Store) Get() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

// Current returns the live config without copying. Callers must not modify
// it; Update always swaps in a new value instead of changing this one.
func (s *Store) Current() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Reload re-reads the config from disk.
func (s *Store) Reload() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	c, err := Parse(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	return nil
}

// Update applies fn to a copy of the config, validates it, saves it and
// makes it live. If fn or validation fails nothing changes.
func (s *Store) Update(fn func(c *Config) error) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	next.applyDefaults()
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if err := s.write(next); err != nil {
		return nil, err
	}
	s.cfg = next
	return next.Clone(), nil
}

// write saves atomically (temp file + rename) with 0600 since it holds keys.
func (s *Store) write(c *Config) error {
	data, err := Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
