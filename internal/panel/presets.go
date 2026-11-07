package panel

import "os/exec"

var lookPath = exec.LookPath

// Preset is a provider template shown when adding a provider.
type Preset struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	BaseURL        string   `json:"base_url"`
	Models         []string `json:"models"`
	MaxTokensField string   `json:"max_tokens_field"`
	APIKeyEnv      string   `json:"api_key_env"`
	Note           string   `json:"note"`
}

// Model lists are left empty on purpose: names change often, and
// "Fetch from provider" reads the live list.
var presets = []Preset{
	{ID: "openai", Name: "OpenAI", Type: "openai", BaseURL: "https://api.openai.com/v1", MaxTokensField: "max_completion_tokens", APIKeyEnv: "OPENAI_API_KEY", Models: []string{}},
	{ID: "openrouter", Name: "OpenRouter", Type: "openai", BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", Models: []string{}, Note: "Hundreds of models behind one key"},
	{ID: "deepseek", Name: "DeepSeek", Type: "openai", BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY", Models: []string{"deepseek-chat", "deepseek-reasoner"}},
	{ID: "deepseek-anthropic", Name: "DeepSeek (Anthropic API)", Type: "anthropic", BaseURL: "https://api.deepseek.com/anthropic", APIKeyEnv: "DEEPSEEK_API_KEY", Models: []string{"deepseek-chat"}, Note: "No translation needed"},
	{ID: "groq", Name: "Groq", Type: "openai", BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY", Models: []string{}},
	{ID: "gemini", Name: "Google Gemini", Type: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", APIKeyEnv: "GEMINI_API_KEY", Models: []string{}},
	{ID: "mistral", Name: "Mistral", Type: "openai", BaseURL: "https://api.mistral.ai/v1", APIKeyEnv: "MISTRAL_API_KEY", Models: []string{}},
	{ID: "xai", Name: "xAI Grok", Type: "openai", BaseURL: "https://api.x.ai/v1", APIKeyEnv: "XAI_API_KEY", Models: []string{}},
	{ID: "zai", Name: "Z.ai GLM (Anthropic API)", Type: "anthropic", BaseURL: "https://api.z.ai/api/anthropic", APIKeyEnv: "ZAI_API_KEY", Models: []string{}},
	{ID: "moonshot", Name: "Moonshot Kimi (Anthropic API)", Type: "anthropic", BaseURL: "https://api.moonshot.ai/anthropic", APIKeyEnv: "MOONSHOT_API_KEY", Models: []string{}},
	{ID: "ollama", Name: "Ollama (local)", Type: "openai", BaseURL: "http://127.0.0.1:11434/v1", Models: []string{}, Note: "No key needed"},
	{ID: "lmstudio", Name: "LM Studio (local)", Type: "openai", BaseURL: "http://127.0.0.1:1234/v1", Models: []string{}, Note: "No key needed"},
	{ID: "opencode", Name: "opencode-bridge (local)", Type: "anthropic", BaseURL: "http://127.0.0.1:4568", Models: []string{}, Note: "providers/opencode in this repo"},
	{ID: "custom-openai", Name: "Custom OpenAI-compatible", Type: "openai", BaseURL: "http://127.0.0.1:8000/v1", Models: []string{}, Note: "new-api, LiteLLM, vLLM, CLIProxyAPI…"},
	{ID: "custom-anthropic", Name: "Custom Anthropic-compatible", Type: "anthropic", BaseURL: "http://127.0.0.1:8000", Models: []string{}},
}
