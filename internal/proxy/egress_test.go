// egress_test.go covers the local-route forwarding path:
// Anthropic body → POST /v1/messages on the URL from the marker → Anthropic
// response relayed back to the client. The proxy is a pure URL forwarder —
// no model rewriting, no provider logic.
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

func makeRouteResolver(t *testing.T) *config.RouteResolver {
	t.Helper()
	r, err := config.NewRouteResolver(&config.Config{})
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

	resolver := makeRouteResolver(t)
	infra := setupInfra(t, resolver)

	marker := fmt.Sprintf("<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:%d -->", port)
	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     marker + " You are helpful",
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

	// Verify the egress received the body AS-IS (no model rewriting).
	captured := getBody()
	var egressReq map[string]interface{}
	if err := json.Unmarshal(captured, &egressReq); err != nil {
		t.Fatalf("parse egress request: %v", err)
	}
	// Model field should pass through unchanged (pure forwarding).
	if egressReq["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("expected model to pass through unchanged, got %v", egressReq["model"])
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

	resolver := makeRouteResolver(t)
	infra := setupInfra(t, resolver)

	marker := fmt.Sprintf("<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:%d -->", port)
	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     marker + " ok",
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

func TestEgressInvalidRouteURL(t *testing.T) {
	// Markers with non-URL values (no http:// prefix) should NOT match the
	// route regex at all — the request goes upstream, not to a local route.
	// This prevents backtick-wrapped examples in CLAUDE.md from being treated
	// as real markers.
	resolver := makeRouteResolver(t)
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 url=not_a_url --> ok",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	status, _, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	// not_a_url does not match https?:// so the marker is ignored and the
	// request is forwarded upstream (echo server returns 200).
	if status != 200 {
		t.Fatalf("expected 200 (upstream passthrough), got %d", status)
	}
}

func TestEgressUnreachable(t *testing.T) {
	// Port 1 is reserved/unreachable.
	resolver := makeRouteResolver(t)
	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:1 --> ok",
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
	if !strings.Contains(errResp.Error.Message, "unreachable") {
		t.Errorf("expected unreachable in message, got %s", errResp.Error.Message)
	}
}

func TestEgressNoResolverFallsBackToStub(t *testing.T) {
	infra := setupInfra(t, nil)
	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:9999 --> ok",
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

	resolver := makeRouteResolver(t)
	infra := setupInfra(t, resolver)

	marker := fmt.Sprintf("<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:%d -->", port)
	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   marker + " ok",
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

// TestEgressAgentHeader was removed: the proxy is now a pure URL forwarder
// and carries no knowledge of agents. Agent name detection belongs to the
// service behind the URL (e.g. opencode-bridge).

func TestEgressBodyPassedAsIs(t *testing.T) {
	srv, port, getBody, _, err := testutil.MockMusistudioServer()
	if err != nil {
		t.Fatalf("mock egress: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	resolver := makeRouteResolver(t)
	infra := setupInfra(t, resolver)

	marker := fmt.Sprintf("<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:%d -->", port)
	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     marker + " ok",
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 16384,
	})
	status, _, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	var egressReq map[string]interface{}
	json.Unmarshal(getBody(), &egressReq)
	// max_tokens should pass through unchanged (no capping)
	mt, _ := egressReq["max_tokens"].(float64)
	if int(mt) != 16384 {
		t.Errorf("expected max_tokens to pass through as 16384, got %v", mt)
	}
	// model should pass through unchanged (no rewriting)
	if egressReq["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("expected model unchanged, got %v", egressReq["model"])
	}
}
