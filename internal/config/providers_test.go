package config

import (
	"os"
	"path/filepath"
	"testing"
)

// loadTestConfig writes yaml to a temp file, loads and resolves it.
func loadTestConfig(t *testing.T, yaml string) (*ProvidersConfig, *ModelResolver) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	r, err := NewModelResolver(cfg)
	if err != nil {
		t.Fatalf("NewModelResolver: %v", err)
	}
	return cfg, r
}

func TestLoadConfigAndResolve(t *testing.T) {
	cfg, r := loadTestConfig(t, `
egress:
  url: http://127.0.0.1:3456
  api_key: test-key
providers:
  - name: ollama
    models:
      fast_coder: qwen3:32b
      reasoning: deepseek-r1:14b
  - name: deepseek
    api_key: tok_123
    models:
      big_coder: deepseek-coder-v2-236b
`)

	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg.Providers))
	}

	m, err := r.Resolve("fast_coder")
	if err != nil {
		t.Fatalf("Resolve fast_coder: %v", err)
	}
	if m.Endpoint != "http://127.0.0.1:3456" {
		t.Errorf("unexpected endpoint: %s", m.Endpoint)
	}
	// Model is rewritten to musistudio "<provider>,<model>" format.
	if m.Model != "ollama,qwen3:32b" {
		t.Errorf("unexpected egress model: %s", m.Model)
	}
	if m.Provider != "ollama" {
		t.Errorf("unexpected provider: %s", m.Provider)
	}
	// Global egress api_key inherited when provider overrides nothing.
	if m.APIKey != "test-key" {
		t.Errorf("expected global egress api key, got %q", m.APIKey)
	}

	m, err = r.Resolve("big_coder")
	if err != nil {
		t.Fatalf("Resolve big_coder: %v", err)
	}
	// Provider-level override wins over global.
	if m.APIKey != "tok_123" {
		t.Errorf("unexpected api key: %s", m.APIKey)
	}
	if m.Model != "deepseek,deepseek-coder-v2-236b" {
		t.Errorf("unexpected egress model: %s", m.Model)
	}

	_, err = r.Resolve("nonexistent")
	if err == nil {
		t.Error("expected error for unknown label")
	}
}

func TestDefaultEgressURL(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: any
    models:
      m: backend
`)
	m, _ := r.Resolve("m")
	if m.Endpoint != "http://127.0.0.1:3456" {
		t.Errorf("expected default egress URL, got %q", m.Endpoint)
	}
}

func TestTrailingSlashTrimmed(t *testing.T) {
	_, r := loadTestConfig(t, `
egress:
  url: https://egress.example.com:3456/
providers:
  - name: p
    endpoint: https://other.example.com/
    models:
      m: backend
`)
	m, _ := r.Resolve("m")
	if m.Endpoint != "https://other.example.com" {
		t.Errorf("expected trimmed override, got %q", m.Endpoint)
	}
}

func TestEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "secret_key_value")

	_, r := loadTestConfig(t, `
egress:
  url: http://127.0.0.1:3456
  api_key: ${TEST_API_KEY}
providers:
  - name: remote
    models:
      test_model: gpt-4
`)

	m, _ := r.Resolve("test_model")
	if m.APIKey != "secret_key_value" {
		t.Errorf("expected expanded key, got %q", m.APIKey)
	}
}

func TestDuplicateModelLabel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
providers:
  - name: a
    models:
      dupe: model-a
  - name: b
    models:
      dupe: model-b
`), 0644)

	cfg, _ := LoadConfig(cfgPath)
	_, err := NewModelResolver(cfg)
	if err == nil {
		t.Error("expected error for duplicate label")
	}
}

func TestModelConfigMaxTokens(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: local
    max_tokens: 4096
    models:
      default_cap: qwen3:32b
      custom_cap:
        model: qwen3:32b
        max_tokens: 8192
`)

	dm, _ := r.Resolve("default_cap")
	if dm.MaxTokens != 4096 {
		t.Errorf("expected provider-level 4096, got %d", dm.MaxTokens)
	}

	cm, _ := r.Resolve("custom_cap")
	if cm.MaxTokens != 8192 {
		t.Errorf("expected per-model 8192, got %d", cm.MaxTokens)
	}
}

func TestCommandProviderNoEndpoint(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: my-agents
    command: "myctl run --agent $AGENT '$PROMPT'"
    models:
      fast: simplifier
`)

	m, err := r.Resolve("fast")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if m.Command != "myctl run --agent $AGENT '$PROMPT'" {
		t.Errorf("unexpected command: %s", m.Command)
	}
	// Command providers keep the bare agent name as Model (no "<prov>,<model>"
	// rewrite) so the existing command-bridge contract survives.
	if m.Model != "simplifier" {
		t.Errorf("unexpected model: %s", m.Model)
	}
}

func TestExplicitCommaModelBypassesPrefix(t *testing.T) {
	// Users who want to hit a specific musistudio router entry directly can
	// pass the comma-form in the YAML and skip the auto-prefix.
	_, r := loadTestConfig(t, `
providers:
  - name: aihubmix
    models:
      glm: Z/glm-4.5,direct
`)
	m, _ := r.Resolve("glm")
	if m.Model != "Z/glm-4.5,direct" {
		t.Errorf("expected comma-form passthrough, got %q", m.Model)
	}
}

