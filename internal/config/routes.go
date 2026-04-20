package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EgressConfig configures shared egress behavior.
type EgressConfig struct {
	Timeout          int    `yaml:"timeout,omitempty"`
	SystemPromptFile string `yaml:"system_prompt_file,omitempty"`
	ReminderFile     string `yaml:"reminder_file,omitempty"`
}

// Config is the top-level config file structure.
type Config struct {
	Egress EgressConfig `yaml:"egress,omitempty"`
}

// RouteResolver holds egress configuration (system prompt, reminder, timeout).
// Route resolution is no longer needed — the URL comes directly from the marker.
type RouteResolver struct {
	systemPrompt string
	reminder     string
	timeout      time.Duration
}

// LoadConfig reads and parses the config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// NewRouteResolver builds a resolver from config.
func NewRouteResolver(cfg *Config) (*RouteResolver, error) {
	var systemPrompt string
	if cfg.Egress.SystemPromptFile != "" {
		path := cfg.Egress.SystemPromptFile
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = home + path[1:]
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("system_prompt_file %q: %w", path, err)
		}
		systemPrompt = strings.TrimSpace(string(data))
	}

	var reminder string
	if cfg.Egress.ReminderFile != "" {
		path := cfg.Egress.ReminderFile
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = home + path[1:]
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reminder_file %q: %w", path, err)
		}
		reminder = strings.TrimSpace(string(data))
	}

	timeout := time.Duration(cfg.Egress.Timeout) * time.Second
	if timeout <= 0 {
		timeout = UpstreamTimeout
	}

	return &RouteResolver{
		systemPrompt: systemPrompt,
		reminder:     reminder,
		timeout:      timeout,
	}, nil
}

// SystemPrompt returns the system prompt override text, or "" if not configured.
func (r *RouteResolver) SystemPrompt() string {
	return r.systemPrompt
}

// Reminder returns the reminder text, or "" if not configured.
func (r *RouteResolver) Reminder() string {
	return r.reminder
}

// Timeout returns the configured header timeout for egress requests.
func (r *RouteResolver) Timeout() time.Duration {
	return r.timeout
}
