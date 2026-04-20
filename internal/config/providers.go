package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ModelConfig is either a plain string ("deepseek-chat") or a map with per-model
// overrides. After the musistudio egress migration, only the backend model name
// plus optional per-model max_tokens remain — provider-quirk transform chains
// and custom params are handled by musistudio itself.
type ModelConfig struct {
	Model     string `yaml:"model"`
	MaxTokens int    `yaml:"max_tokens,omitempty"`
}

// UnmarshalYAML allows ModelConfig to be a plain string or a map.
func (mc *ModelConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		mc.Model = value.Value
		return nil
	}
	type raw ModelConfig
	return value.Decode((*raw)(mc))
}

// ProviderConfig represents either:
//   - a musistudio-routed provider (Endpoint+APIKey empty → uses egress default, or points at musistudio)
//   - a command bridge (Command non-empty; short-circuits all HTTP forwarding)
//
// Endpoint is kept so a non-default musistudio instance (e.g. remote host) or a
// plain command bridge stanza can coexist with the default local 3456 server.
type ProviderConfig struct {
	Name         string                 `yaml:"name"`
	Endpoint     string                 `yaml:"endpoint,omitempty"` // musistudio URL; empty = use global Egress.URL
	Command      string                 `yaml:"command,omitempty"`  // shell command template; skips endpoint entirely
	APIKey       string                 `yaml:"api_key,omitempty"`
	MaxTokens    int                    `yaml:"max_tokens,omitempty"`
	Default      bool                   `yaml:"default,omitempty"`
	DefaultModel string                 `yaml:"default_model,omitempty"`
	Models       map[string]ModelConfig `yaml:"models"` // label → backend model name or config
}

// EgressConfig configures the shared musistudio egress endpoint.
// When a provider has no explicit endpoint/api_key, these values are used.
type EgressConfig struct {
	URL              string `yaml:"url,omitempty"`                // default: http://127.0.0.1:3456
	APIKey           string `yaml:"api_key,omitempty"`            // forwarded as x-api-key
	SystemPromptFile string `yaml:"system_prompt_file,omitempty"` // file path; replaces the system field in every routed request
	ReminderFile     string `yaml:"reminder_file,omitempty"`      // file path; injected as <system-reminder> into tool_result messages
	Timeout          int    `yaml:"timeout,omitempty"`            // seconds; time to wait for first response byte (default 30)
}

// ProvidersConfig is the top-level config file structure.
type ProvidersConfig struct {
	Egress    EgressConfig     `yaml:"egress,omitempty"`
	Providers []ProviderConfig `yaml:"providers"`
}

// ResolvedModel holds the result of resolving a model label. All fields are
// shaped around the post-musistudio flow: Endpoint is the musistudio base URL,
// Model is "<provider>,<backend-model>" (the musistudio model-selection format),
// APIKey is forwarded as x-api-key on the POST /v1/messages call.
type ResolvedModel struct {
	Endpoint  string // musistudio base URL (e.g. http://127.0.0.1:3456)
	Model     string // "<provider-name>,<backend-model>" in musistudio terms
	Command   string // shell command template (empty = HTTP forward to Endpoint)
	APIKey    string // musistudio x-api-key value
	Label     string // original label, e.g. "fast_coder"
	Provider  string // provider name as declared in the YAML (logging only)
	MaxTokens int    // cap max_tokens (0 = no cap)
}

// ModelResolver resolves model labels to provider details.
type ModelResolver struct {
	models         map[string]ResolvedModel
	defaultModel   *ResolvedModel            // provider with default: true
	commandBridges map[string]string          // provider-name → command template
	systemPrompt   string                    // if set, replaces the system field in routed requests
	reminder       string                    // if set, injected into tool_result messages as <system-reminder>
	timeout        time.Duration             // time to wait for first response byte from egress
}

var envVarRE = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnvVars replaces ${VAR} references with environment variable values.
func expandEnvVars(s string) string {
	return envVarRE.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarRE.FindStringSubmatch(match)[1]
		return os.Getenv(varName)
	})
}

// LoadConfig reads and parses a config file.
func LoadConfig(path string) (*ProvidersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ProvidersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// defaultEgressURL is used when neither provider.endpoint nor egress.url is set.
const defaultEgressURL = "http://127.0.0.1:3456"

// NewModelResolver builds a resolver from config.
func NewModelResolver(cfg *ProvidersConfig) (*ModelResolver, error) {
	globalURL := strings.TrimRight(cfg.Egress.URL, "/")
	if globalURL == "" {
		globalURL = defaultEgressURL
	}
	globalKey := expandEnvVars(cfg.Egress.APIKey)

	models := make(map[string]ResolvedModel)
	var defaultModel *ResolvedModel
	commandBridges := make(map[string]string)

	for _, p := range cfg.Providers {
		if p.Name == "" {
			return nil, fmt.Errorf("provider missing name")
		}

		endpoint := strings.TrimRight(p.Endpoint, "/")
		if endpoint == "" {
			endpoint = globalURL
		}
		apiKey := expandEnvVars(p.APIKey)
		if apiKey == "" {
			apiKey = globalKey
		}

		if p.Command != "" {
			commandBridges[p.Name] = p.Command
		}

		for label, mc := range p.Models {
			if _, exists := models[label]; exists {
				return nil, fmt.Errorf("duplicate model label %q", label)
			}
			maxTokens := p.MaxTokens
			if mc.MaxTokens > 0 {
				maxTokens = mc.MaxTokens
			}
			// musistudio model selection format: "<provider>,<backend-model>"
			// Fall back to just the backend name when caller supplied one
			// explicitly containing a comma (lets power users bypass the
			// provider prefix, e.g. for custom routers).
			musistudioModel := mc.Model
			if p.Command == "" && !strings.Contains(musistudioModel, ",") {
				musistudioModel = fmt.Sprintf("%s,%s", p.Name, mc.Model)
			}
			models[label] = ResolvedModel{
				Endpoint:  endpoint,
				Model:     musistudioModel,
				Command:   p.Command,
				APIKey:    apiKey,
				Label:     label,
				Provider:  p.Name,
				MaxTokens: maxTokens,
			}
		}

		if p.Default && defaultModel == nil {
			targetLabel := p.DefaultModel
			if targetLabel == "" {
				// First alphabetically
				labels := make([]string, 0, len(p.Models))
				for l := range p.Models {
					labels = append(labels, l)
				}
				sort.Strings(labels)
				if len(labels) > 0 {
					targetLabel = labels[0]
				}
			}
			if targetLabel != "" {
				if dm, ok := models[targetLabel]; ok {
					dm.Label = "direct"
					defaultModel = &dm
				}
			}
		}
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

	return &ModelResolver{
		models:         models,
		defaultModel:   defaultModel,
		commandBridges: commandBridges,
		systemPrompt:   systemPrompt,
		reminder:       reminder,
		timeout:        timeout,
	}, nil
}

// SystemPrompt returns the system prompt override text, or "" if not configured.
func (r *ModelResolver) SystemPrompt() string {
	return r.systemPrompt
}

// Reminder returns the reminder text, or "" if not configured.
func (r *ModelResolver) Reminder() string {
	return r.reminder
}

// Timeout returns the configured header timeout for egress requests.
func (r *ModelResolver) Timeout() time.Duration {
	return r.timeout
}

// Resolve looks up a model label and returns its provider details.
func (r *ModelResolver) Resolve(label string) (ResolvedModel, error) {
	m, ok := r.models[label]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("unknown model label %q", label)
	}
	return m, nil
}

// ResolveDefault returns the model marked as default (provider with default: true).
func (r *ModelResolver) ResolveDefault() (ResolvedModel, error) {
	if r.defaultModel == nil {
		return ResolvedModel{}, fmt.Errorf("no default provider configured (set default: true)")
	}
	return *r.defaultModel, nil
}

// ResolveCommand builds a ResolvedModel for a command bridge provider with a
// dynamic agent name. The agent is stored in Model so the command template can
// substitute $AGENT at execution time.
func (r *ModelResolver) ResolveCommand(providerName, agentName string) (ResolvedModel, error) {
	cmd, ok := r.commandBridges[providerName]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("no command bridge provider %q", providerName)
	}
	return ResolvedModel{
		Command:  cmd,
		Model:    agentName,
		Label:    providerName,
		Provider: providerName,
	}, nil
}
