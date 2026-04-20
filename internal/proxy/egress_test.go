// egress_test.go covers the post-musistudio local-route forwarding path:
// Anthropic body → POST /v1/messages on the configured egress → Anthropic
// response relayed back to the client. Replaces the former OpenAI-translation
// tests (moved to .deleted/).
package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/testutil"
)

// anthropicResponse is a minimal shape sufficient for these tests.
type anthropicResponse struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	Model      string `json:"model"`
	Content    []anthropicBlock
	StopReason *string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

func makeResolver(t *testing.T, egressURL, providerName, label, backendModel string) *config.ModelResolver {
	t.Helper()
	r, err := config.NewModelResolver(&config.ProvidersConfig{
		Egress: config.EgressConfig{URL: egressURL},
		Providers: []config.ProviderConfig{{
			Name:   providerName,
			Models: map[string]config.ModelConfig{label: {Model: backendModel}},
		}},
	})
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}
	return r
}

func TestEgressNonStreaming(t *testing.T) {
	srv, port, getBody, _, err := testutil.MockMusistudioServer()
	if err != nil {
		t.Fatalf("mock egress: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resolver := makeResolver(t, fmt.Sprintf("http://127.0.0.1:%d", port), "deepseek", "fast", "deepseek-chat")
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=fast --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
	})

	status, respBody, contentType := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("expected JSON content type, got %s", contentType)
	}

	// Verify the egress received the rewritten model field.
	captured := getBody()
	var egressReq map[string]interface{}
	if err := json.Unmarshal(captured, &egressReq); err != nil {
		t.Fatalf("parse egress request: %v", err)
	}
	if egressReq["model"] != "deepseek,deepseek-chat" {
		t.Errorf("expected model rewritten to 'deepseek,deepseek-chat', got %v", egressReq["model"])
	}

	// Verify the Anthropic response was relayed verbatim.
	var resp anthropicResponse
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("parse response: %v\nbody: %s", err, respBody)
	}
	if resp.Type != "message" || resp.Role != "assistant" {
		t.Errorf("unexpected type/role: %s/%s", resp.Type, resp.Role)
	}
}

func TestEgressStreamingPassthrough(t *testing.T) {
	srv, port, _, _, err := testutil.MockMusistudioServer()
	if err != nil {
		t.Fatalf("mock egress: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resolver := makeResolver(t, fmt.Sprintf("http://127.0.0.1:%d", port), "groq", "fast", "llama-3.3")
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=fast --> ok",
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1024,
		"stream":     true,
	})

	status, respBody, contentType := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected SSE content type, got %s", contentType)
	}
	// All 6 Anthropic lifecycle events should pass through unchanged.
	assertSSELifecycle(t, respBody)
	if !strings.Contains(respBody, `"stop_reason":"end_turn"`) {
		t.Error("missing end_turn stop_reason")
	}
}

func TestEgressToolUsePassthrough(t *testing.T) {
	srv, port, _, _, err := testutil.MockMusistudioServer()
	if err != nil {
		t.Fatalf("mock egress: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resolver := makeResolver(t, fmt.Sprintf("http://127.0.0.1:%d", port), "deepseek", "tool_model", "deepseek-chat")
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=tool_model --> ok",
		"messages":   []map[string]string{{"role": "user", "content": "read file"}},
		"max_tokens": 1024,
		"tools": []map[string]interface{}{{
			"name":         "Read",
			"description":  "Read a file",
			"input_schema": map[string]interface{}{"type": "object"},
		}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	var resp anthropicResponse
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	var toolBlock *anthropicBlock
	for i := range resp.Content {
		if resp.Content[i].Type == "tool_use" {
			toolBlock = &resp.Content[i]
			break
		}
	}
	if toolBlock == nil {
		t.Fatalf("expected tool_use block, got body: %s", respBody)
	}
	if toolBlock.Name != "Read" {
		t.Errorf("expected tool name Read, got %s", toolBlock.Name)
	}
	if toolBlock.Input["file_path"] != "/tmp/test.txt" {
		t.Errorf("unexpected input: %v", toolBlock.Input)
	}
	if resp.StopReason == nil || *resp.StopReason != "tool_use" {
		t.Errorf("expected stop_reason tool_use, got %v", resp.StopReason)
	}
}

func TestEgressUnknownModelLabel(t *testing.T) {
	resolver := makeResolver(t, "http://127.0.0.1:1", "p", "known", "backend")
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=unknown --> ok",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 400 {
		t.Fatalf("expected 400, got %d: %s", status, respBody)
	}
	var errResp aErrorResponse
	if err := json.Unmarshal([]byte(respBody), &errResp); err != nil {
		t.Fatalf("parse error response: %v\nbody: %s", err, respBody)
	}
	if errResp.Type != "error" {
		t.Errorf("expected type error, got %s", errResp.Type)
	}
	if !strings.Contains(errResp.Error.Message, "unknown") {
		t.Errorf("expected model label in error, got: %s", errResp.Error.Message)
	}
}

func TestEgressUnreachable(t *testing.T) {
	// Port 1 is reserved/unreachable.
	resolver := makeResolver(t, "http://127.0.0.1:1", "dead", "dead_model", "x")
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=dead_model --> ok",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 502 {
		t.Fatalf("expected 502, got %d: %s", status, respBody)
	}
	var errResp aErrorResponse
	if err := json.Unmarshal([]byte(respBody), &errResp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if errResp.Type != "error" {
		t.Errorf("expected error type, got %s", errResp.Type)
	}
	if !strings.Contains(errResp.Error.Message, "dead_model") {
		t.Errorf("expected label in message, got %s", errResp.Error.Message)
	}
}

func TestEgressNoResolverFallsBackToStub(t *testing.T) {
	infra := setupInfra(t, nil)
	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_model --> ok",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200 stub, got %d", status)
	}
	if !strings.Contains(respBody, "no local provider configured") {
		t.Error("expected stub response when resolver is nil")
	}
}

func TestEgressForwardsApiKey(t *testing.T) {
	srv, port, _, getHeaders, err := testutil.MockMusistudioServer()
	if err != nil {
		t.Fatalf("mock egress: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Egress: config.EgressConfig{
			URL:    fmt.Sprintf("http://127.0.0.1:%d", port),
			APIKey: "egress-secret",
		},
		Providers: []config.ProviderConfig{{
			Name:   "openrouter",
			Models: map[string]config.ModelConfig{"m": {Model: "some-model"}},
		}},
	})
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=m --> ok",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	// Client sends its own Claude auth headers; the proxy should NOT
	// forward those to the egress — only the configured key.
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, map[string]string{
		"x-api-key":         "sk-ant-CLAUDE_SECRET",
		"Authorization":     "Bearer sk-ant-CLAUDE_SECRET",
		"anthropic-beta":    "max-tokens-3-5-sonnet-2024-07-15",
		"anthropic-version": "2023-06-01",
	})
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	headers := getHeaders()
	if headers == nil {
		t.Fatal("egress got no request")
	}
	got := headers.Get("X-Api-Key")
	if got != "egress-secret" {
		t.Errorf("expected egress key, got %q", got)
	}
	// Claude's Authorization header must NOT be passed along to the egress.
	if auth := headers.Get("Authorization"); strings.Contains(auth, "sk-ant-") {
		t.Errorf("Claude's Authorization header leaked to egress: %s", auth)
	}
	if v := headers.Get("Anthropic-Beta"); v != "" {
		// The proxy does not copy client headers on the local-route path;
		// only the ones it sets itself travel to the egress.
		t.Errorf("anthropic-beta leaked: %s", v)
	}
}

func TestEgressRewritesMaxTokensCap(t *testing.T) {
	srv, port, getBody, _, err := testutil.MockMusistudioServer()
	if err != nil {
		t.Fatalf("mock egress: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Egress: config.EgressConfig{URL: fmt.Sprintf("http://127.0.0.1:%d", port)},
		Providers: []config.ProviderConfig{{
			Name:      "p",
			MaxTokens: 4096, // cap
			Models:    map[string]config.ModelConfig{"m": {Model: "backend"}},
		}},
	})
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=m --> ok",
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 16384, // exceeds cap
	})
	status, _, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	var egressReq map[string]interface{}
	json.Unmarshal(getBody(), &egressReq)
	mt, _ := egressReq["max_tokens"].(float64)
	if int(mt) != 4096 {
		t.Errorf("expected max_tokens capped to 4096, got %v", mt)
	}
}

func TestEgressForwardsStructuredErrorBody(t *testing.T) {
	// Egress that returns an Anthropic-shaped 400 should be forwarded verbatim.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"custom egress error"}}`))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	resolver := makeResolver(t, fmt.Sprintf("http://127.0.0.1:%d", port), "p", "m", "backend")
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=m --> ok",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}
	if !strings.Contains(respBody, "custom egress error") {
		t.Errorf("expected egress's own error message preserved, got %s", respBody)
	}
}
