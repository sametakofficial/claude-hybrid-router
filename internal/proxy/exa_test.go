package proxy

import (
	"strings"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
)

func TestBuildExaEnvelopeDefaultNoFullText(t *testing.T) {
	toolOutput := `{"results":[{"id":"src_1","title":"Prompt Injection Paper","url":"https://arxiv.org/abs/2302.12173","summary":"Shows indirect prompt injection against LLM-integrated applications.","highlights":["Indirect prompt injection can hijack downstream behavior."]}]}`
	env, ok := buildExaEnvelope(toolOutput, config.ExaConfig{Mode: "envelope", MinHighlightChars: 40})
	if !ok {
		t.Fatal("expected envelope conversion")
	}
	if env.Provider != "exa" || env.Mode != "envelope" {
		t.Fatalf("unexpected envelope metadata: %+v", env)
	}
	if env.FullTextIncluded {
		t.Fatal("expected full text to stay disabled by default")
	}
	if env.Results[0].Text != nil {
		t.Fatalf("expected nil text field, got %#v", env.Results[0].Text)
	}
	if env.Results[0].Signals.LowSignal {
		t.Fatal("did not expect low-signal classification")
	}
}

func TestBuildExaEnvelopeLowSignalFallback(t *testing.T) {
	toolOutput := `{"results":[{"id":"src_1","title":"Sparse Result","url":"https://example.com/report.pdf","summary":"","highlights":[],"text":"full raw document text"}]}`
	env, ok := buildExaEnvelope(toolOutput, config.ExaConfig{
		Mode:                "envelope",
		AllowFullText:       true,
		FullTextOnLowSignal: true,
	})
	if !ok {
		t.Fatal("expected envelope conversion")
	}
	if !env.FullTextIncluded {
		t.Fatal("expected low-signal fallback to include bounded text")
	}
	if env.FallbackReason != "low_signal" {
		t.Fatalf("expected low_signal fallback, got %q", env.FallbackReason)
	}
	if env.Results[0].Text == nil || *env.Results[0].Text != "full raw document text" {
		t.Fatalf("expected bounded text, got %#v", env.Results[0].Text)
	}
	if !env.Results[0].Signals.LowSignal {
		t.Fatal("expected low-signal flag")
	}
}

func TestMaybeNormalizeExaToolResultPassthroughOnInvalidJSON(t *testing.T) {
	got := maybeNormalizeExaToolResult("not-json", config.ResolvedModel{Provider: "exa"})
	if got != "not-json" {
		t.Fatalf("expected passthrough for invalid JSON, got %q", got)
	}
}

func TestClipHighlightsRespectsBudget(t *testing.T) {
	clipped := clipHighlights([]string{"abcdef", "ghijkl"}, 8)
	if strings.Join(clipped, "") != "abcdefgh" {
		t.Fatalf("unexpected clipped highlights: %#v", clipped)
	}
}
