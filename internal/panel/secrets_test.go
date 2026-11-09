package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func TestMaskKeyShowsAtMostFour(t *testing.T) {
	for in, want := range map[string]string{"": "", "short": maskedToken, "sk-secret-123456789": "…6789"} {
		if got := maskKey(in); got != want {
			t.Errorf("maskKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnmaskProvider(t *testing.T) {
	stored := config.Provider{ID: "x", APIKey: "sk-stored-1234567", Headers: map[string]string{"X-Secret": "hdr-secret-value", "X-Old": "o"}}
	sent := config.Provider{ID: "x", APIKey: maskKey(stored.APIKey), Headers: map[string]string{"X-Secret": maskKey("hdr-secret-value"), "X-New": "plain"}}
	got, dropped := unmaskProvider(sent, &stored, false)
	if dropped || got.APIKey != stored.APIKey || got.Headers["X-Secret"] != "hdr-secret-value" || got.Headers["X-New"] != "plain" {
		t.Fatalf("masked values must keep stored ones: %+v", got)
	}
	if _, ok := got.Headers["X-Old"]; ok {
		t.Fatal("a header the browser removed must be removed")
	}
	if got, _ := unmaskProvider(config.Provider{}, &stored, false); got.APIKey != stored.APIKey {
		t.Fatal("blank key keeps the stored key")
	}
	if got, _ := unmaskProvider(sent, &stored, true); got.APIKey != "" {
		t.Fatal("clear_api_key must remove the key")
	}
	// Before the fix a long mask on a new provider was saved as the key.
	got, dropped = unmaskProvider(config.Provider{APIKey: "sk-…6789", Headers: map[string]string{"A": "…abcd"}}, nil, false)
	if !dropped || got.APIKey != "" || len(got.Headers) != 0 {
		t.Fatalf("new provider must never store a mask: %+v", got)
	}
}

func TestProviderSecretsOverAPI(t *testing.T) {
	p, h, _ := setup(t)
	p.Store.Update(func(c *config.Config) error {
		c.Providers[0].Headers = map[string]string{"X-Api-Token": "very-secret-header"}
		return nil
	})
	_, out := do(h, "GET", "/api/providers", "", nil)
	if strings.Contains(out, "very-secret") || strings.Contains(out, "secret-123") {
		t.Fatalf("secret leaked: %s", out)
	}
	_, raw := do(h, "GET", "/api/config/raw", "", nil)
	if strings.Contains(raw, "very-secret") {
		t.Fatalf("raw config leaks header: %s", raw)
	}
	// Send everything back masked: nothing changes.
	code, out := do(h, "PUT", "/api/providers/gpt", `{"name":"GPT","type":"openai","base_url":"https://x/v1","api_key":"…6789","headers":{"X-Api-Token":"…ader"},"models":["m"],"enabled":true}`, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	pr, _ := p.Store.Current().Provider("gpt")
	if pr.APIKey != "sk-secret-123456789" || pr.Headers["X-Api-Token"] != "very-secret-header" {
		t.Fatalf("stored secrets lost: %+v", pr)
	}
	// Clear the key.
	do(h, "PUT", "/api/providers/gpt", `{"type":"openai","base_url":"https://x/v1","clear_api_key":true,"models":["m"],"enabled":true}`, nil)
	if pr, _ := p.Store.Current().Provider("gpt"); pr.APIKey != "" {
		t.Fatalf("key not cleared: %+v", pr)
	}
	// New provider with a masked key (e.g. a duplicated card).
	do(h, "PUT", "/api/providers/copy?create=1", `{"type":"openai","base_url":"https://x/v1","api_key":"…6789","models":["m"],"enabled":true}`, nil)
	if pr, _ := p.Store.Current().Provider("copy"); pr.APIKey != "" {
		t.Fatalf("mask saved as key: %+v", pr)
	}
}

func TestRawRenameWithMaskedKeyIsRefused(t *testing.T) {
	p, h, _ := setup(t)
	_, out := do(h, "GET", "/api/config/raw", "", nil)
	var got struct{ YAML string }
	json.Unmarshal([]byte(out), &got)
	yaml := strings.Replace(got.YAML, "id: gpt", "id: gpt2", 1)
	code, res := do(h, "PUT", "/api/config/raw", `{"yaml":`+jsonString(yaml)+`}`, nil)
	if code != 400 || !strings.Contains(res, "masked") {
		t.Fatalf("want 400 for renamed provider with a masked key, got %d %s", code, res)
	}
	if _, ok := p.Store.Current().Provider("gpt"); !ok {
		t.Fatal("config must not change")
	}
}

func TestProviderEnabledEndpoint(t *testing.T) {
	p, h, _ := setup(t)
	code, out := do(h, "PUT", "/api/providers/gpt/enabled", `{"enabled":false}`, nil)
	if code != 200 || !strings.Contains(out, `"enabled":false`) || strings.Contains(out, "secret") {
		t.Fatalf("%d %s", code, out)
	}
	pr, _ := p.Store.Current().Provider("gpt")
	if pr.Enabled || pr.APIKey != "sk-secret-123456789" || pr.BaseURL != "https://x/v1" {
		t.Fatalf("only the flag may change: %+v", pr)
	}
	if code, _ := do(h, "PUT", "/api/providers/nope/enabled", `{"enabled":true}`, nil); code != 404 {
		t.Fatalf("unknown provider: %d", code)
	}
	if code, _ := do(h, "PUT", "/api/providers/gpt/enabled", `{}`, nil); code != 400 {
		t.Fatalf("missing flag: %d", code)
	}
}
