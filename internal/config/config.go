// Package config holds the claude-hybrid configuration schema and a
// thread-safe store that loads and saves it atomically.
package config

import (
	"os"
	"path"

	"gopkg.in/yaml.v3"
)

// Provider types.
const (
	TypeOpenAI    = "openai"    // OpenAI-compatible /chat/completions (translated)
	TypeAnthropic = "anthropic" // Anthropic-compatible /v1/messages (passed through)
)

// ProviderTypes lists every valid provider type. internal/provider has the
// matching adapters; a test there keeps the two lists in sync.
var ProviderTypes = []string{TypeOpenAI, TypeAnthropic}

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
	HeaderTimeout int    `yaml:"header_timeout" json:"header_timeout"` // seconds to wait for response headers (Anthropic and providers); needs a restart
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

// Provider returns the provider with the given id.
func (c *Config) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
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

// Clone returns a deep copy so callers can mutate it freely.
func (c *Config) Clone() *Config {
	out, _ := yaml.Marshal(c)
	var n Config
	_ = yaml.Unmarshal(out, &n)
	n.setDefaults()
	return &n
}
