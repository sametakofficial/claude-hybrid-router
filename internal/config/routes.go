package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RouteConfig maps a route name to a target URL.
type RouteConfig struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// RouteEgressConfig configures shared egress behavior.
type RouteEgressConfig struct {
	Timeout          int    `yaml:"timeout,omitempty"`
	SystemPromptFile string `yaml:"system_prompt_file,omitempty"`
	ReminderFile     string `yaml:"reminder_file,omitempty"`
}

// RoutesConfig is the top-level config file structure.
type RoutesConfig struct {
	Egress RouteEgressConfig `yaml:"egress,omitempty"`
	Routes []RouteConfig     `yaml:"routes"`
}

// RouteResolver resolves route names to target URLs.
type RouteResolver struct {
	routes       map[string]string
	systemPrompt string
	reminder     string
	timeout      time.Duration
}

// LoadRoutesConfig reads and parses a routes config file.
func LoadRoutesConfig(path string) (*RoutesConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg RoutesConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// NewRouteResolver builds a resolver from config.
func NewRouteResolver(cfg *RoutesConfig) (*RouteResolver, error) {
	routes := make(map[string]string, len(cfg.Routes))
	for _, r := range cfg.Routes {
		if r.Name == "" {
			return nil, fmt.Errorf("route missing name")
		}
		if r.URL == "" {
			return nil, fmt.Errorf("route %q missing url", r.Name)
		}
		if _, exists := routes[r.Name]; exists {
			return nil, fmt.Errorf("duplicate route name %q", r.Name)
		}
		routes[r.Name] = strings.TrimRight(r.URL, "/")
	}

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
		routes:       routes,
		systemPrompt: systemPrompt,
		reminder:     reminder,
		timeout:      timeout,
	}, nil
}

// Resolve looks up a route name and returns its target URL.
func (r *RouteResolver) Resolve(name string) (string, error) {
	url, ok := r.routes[name]
	if !ok {
		return "", fmt.Errorf("unknown route %q", name)
	}
	return url, nil
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
