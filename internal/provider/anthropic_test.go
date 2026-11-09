package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func parse(t *testing.T, s string) anthropic.Request {
	t.Helper()
	r, err := anthropic.ParseRequest([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPrepareAnthropicClampsThinking(t *testing.T) {
	// Before: max_tokens was capped to 8000 but budget_tokens stayed 31999 -> 400.
	r := PrepareAnthropic(parse(t, `{"model":"m","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":31999}}`), config.Route{MaxTokens: 8000})
	th := r["thinking"].(map[string]any)
	if b, _ := anthropic.Request(th).Int("budget_tokens"); b != 4000 || r["max_tokens"] != 8000 {
		t.Fatalf("budget %d max %v", b, r["max_tokens"])
	}
	r = PrepareAnthropic(parse(t, `{"max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":31999}}`), config.Route{MaxTokens: 1500})
	if _, ok := r["thinking"]; ok {
		t.Fatal("thinking must be dropped when the cap leaves no valid budget")
	}
	r = PrepareAnthropic(parse(t, `{"max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":2000}}`), config.Route{MaxTokens: 8000})
	if b, _ := anthropic.Request(r["thinking"].(map[string]any)).Int("budget_tokens"); b != 2000 {
		t.Fatalf("a budget that fits stays: %d", b)
	}
}

func TestPrepareAnthropicStripsBetaFields(t *testing.T) {
	r := PrepareAnthropic(parse(t, `{"model":"m","max_tokens":10,
		"context_management":{"edits":[]},"output_config":{"effort":"high"},"container":"x","mcp_servers":[],
		"metadata":{"user_id":"u","extra":1},
		"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"tools":[{"name":"Read","input_schema":{"type":"object"},"defer_loading":true,"cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"web_search_20250305","name":"web_search","max_uses":3}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`), config.Route{Model: "glm"})
	b, _ := json.Marshal(r)
	s := string(b)
	for _, bad := range []string{"context_management", "output_config", "container", "mcp_servers", `"extra"`, `"ttl"`, "defer_loading"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s not stripped: %s", bad, s)
		}
	}
	for _, good := range []string{`"model":"glm"`, `"user_id":"u"`, `"ephemeral"`, `"max_uses":3`, `"input_schema"`} {
		if !strings.Contains(s, good) {
			t.Errorf("%s missing: %s", good, s)
		}
	}
}

func TestAnthropicAdapter(t *testing.T) {
	var got *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, gotBody = r, string(b)
		switch r.URL.Path {
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":5,"output_tokens":1}}`)
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ad := newAnthropic(config.Provider{ID: "glm", BaseURL: srv.URL + "/v1", APIKey: "glm-key", Headers: map[string]string{"X-Extra": "1"}}, NewClient(NewTransport(time.Second)))
	reply, err := ad.Messages(context.Background(), &Request{Body: parse(t, `{"model":"m","max_tokens":100,"messages":[]}`), Route: config.Route{Model: "glm-4.6"}, Query: "beta=true"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	u, err := reply.WriteTo(rec)
	reply.Close()
	if err != nil || u.Input != 5 || !strings.Contains(rec.Body.String(), "OK") {
		t.Fatalf("%v %+v %s", err, u, rec.Body)
	}
	if got.Header.Get("x-api-key") != "glm-key" || got.Header.Get("X-Extra") != "1" || got.Header.Get("anthropic-version") != defaultVersion || got.URL.RawQuery != "beta=true" {
		t.Fatalf("headers %v query %q", got.Header, got.URL.RawQuery)
	}
	if !strings.Contains(gotBody, `"glm-4.6"`) {
		t.Fatal(gotBody)
	}
	// count_tokens is missing on this server: fall back to an estimate.
	if n, err := ad.CountTokens(context.Background(), &Request{Body: parse(t, `{"model":"m","messages":[{"role":"user","content":"hello"}]}`)}); err != nil || n < 1 {
		t.Fatalf("count %d %v", n, err)
	}
	if m, err := ad.ListModels(context.Background()); err != nil || strings.Join(m, ",") != "a,b" {
		t.Fatalf("models %v %v", m, err)
	}
}
