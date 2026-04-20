package proxy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
)

func TestExaSafeWrapperLive(t *testing.T) {
	if os.Getenv("EXA_LIVE_TEST") != "1" {
		t.Skip("set EXA_LIVE_TEST=1 to run live Exa integration")
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to locate current test file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	wrapper := filepath.Join(repoRoot, "deploy", "exa-safe-wrapper.py")

	cmd := exec.Command(wrapper, "--query", "site:exa.ai search api")
	cmd.Env = append(os.Environ(), "EXA_API_KEY=7bdc3c31-8c45-4002-b387-980732c63cd6")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wrapper execution failed: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("wrapper returned invalid JSON: %v", err)
	}
	results, ok := raw["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("expected non-empty Exa results, got %T %#v", raw["results"], raw["results"])
	}

	env, ok := buildExaEnvelope(string(out), config.ExaConfig{
		Mode:                "envelope",
		DefaultFullText:     false,
		AllowFullText:       true,
		FullTextOnLowSignal: true,
		MinHighlightChars:   40,
	})
	if !ok {
		t.Fatal("expected live Exa payload to normalize into envelope")
	}
	if len(env.Results) == 0 {
		t.Fatal("expected at least one normalized result")
	}
	if env.Results[0].URL == "" || env.Results[0].Title == "" {
		t.Fatalf("expected title/url in first result, got %+v", env.Results[0])
	}
	if env.Results[0].Text != nil {
		t.Fatalf("expected default full text to remain disabled, got %+v", env.Results[0])
	}
	if len(env.Results[0].Highlights) == 0 && env.Results[0].Summary == "" {
		t.Fatalf("expected summary or highlights in normalized result, got %+v", env.Results[0])
	}
	if _, ok := raw["fallbackReason"]; ok {
		t.Logf("live wrapper triggered fallback: %v", raw["fallbackReason"])
	}
}
