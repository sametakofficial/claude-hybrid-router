package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDetectLocalRoute_StringSystem(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "<!-- @proxy-local-route:af83e9 url=http://localhost:3456 --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "http://localhost:3456" {
		t.Fatalf("expected http://localhost:3456, got %q", route.Route)
	}

	var data map[string]interface{}
	json.Unmarshal(stripped, &data)
	sys := data["system"].(string)
	if sys != "You are helpful" {
		t.Errorf("expected stripped system, got %q", sys)
	}
}

func TestDetectLocalRoute_ListSystem(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system": []map[string]string{
			{"type": "text", "text": "<!-- @proxy-local-route:af83e9 url=http://localhost:4567 --> Instructions"},
		},
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "http://localhost:4567" {
		t.Fatalf("expected http://localhost:4567, got %q", route.Route)
	}

	var data map[string]interface{}
	json.Unmarshal(stripped, &data)
	blocks := data["system"].([]interface{})
	text := blocks[0].(map[string]interface{})["text"].(string)
	if text != "Instructions" {
		t.Errorf("expected stripped text, got %q", text)
	}
}

func TestDetectLocalRoute_NoMarker(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "" {
		t.Fatalf("expected no route, got %q", route.Route)
	}
	if !bytes.Equal(stripped, body) {
		t.Error("body should be unchanged")
	}
}

// TestDetectLocalRoute_MarkerInMessages verifies that markers in user
// messages ARE detected. Claude Code packages CLAUDE.md content as
// <system-reminder> blocks inside user messages (not in the top-level
// `system` field), so the marker must be picked up there too.
func TestDetectLocalRoute_MarkerInMessages(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{{
			"role":    "user",
			"content": "<!-- @proxy-local-route:af83e9 url=http://localhost:3456 --> hello",
		}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "http://localhost:3456" {
		t.Fatalf("expected route=http://localhost:3456, got %q", route.Route)
	}
	if bytes.Equal(stripped, body) {
		t.Error("body should have marker stripped")
	}
	if bytes.Contains(stripped, []byte("@proxy-local-route")) {
		t.Error("stripped body still contains marker")
	}
}

func TestDetectLocalRoute_NonJSON(t *testing.T) {
	body := []byte("not json at all")
	route, stripped := detectLocalRoute(body)
	if route.Route != "" {
		t.Fatalf("expected no route, got %q", route.Route)
	}
	if !bytes.Equal(stripped, body) {
		t.Error("body should be unchanged")
	}
}

func TestDetectLocalRoute_EmptyBody(t *testing.T) {
	route, stripped := detectLocalRoute(nil)
	if route.Route != "" || stripped != nil {
		t.Error("expected nil passthrough")
	}
}

func TestRewriteSystemPrompt_PreservesOpencodeAgentMarker(t *testing.T) {
	// Simulate the flow: system has both route marker and agent marker.
	// After detectLocalRoute strips the route marker, the agent marker remains.
	// Then rewriteSystemPrompt replaces the system but must preserve the agent marker.
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:4568 -->\n<!-- @opencode-agent:simplifier -->",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "http://127.0.0.1:4568" {
		t.Fatalf("expected route http://127.0.0.1:4568, got %q", route.Route)
	}

	// After stripping, the agent marker should still be in the system field.
	var preRewrite map[string]interface{}
	json.Unmarshal(stripped, &preRewrite)
	sys := preRewrite["system"].(string)
	if !strings.Contains(sys, "@opencode-agent:simplifier") {
		t.Fatalf("agent marker lost after detectLocalRoute, system=%q", sys)
	}

	// Now rewrite the system prompt — the agent marker must survive.
	newPrompt := "You are a helpful assistant."
	result := rewriteSystemPrompt(stripped, newPrompt)

	var postRewrite map[string]interface{}
	json.Unmarshal(result, &postRewrite)
	finalSys := postRewrite["system"].(string)
	if !strings.Contains(finalSys, newPrompt) {
		t.Errorf("new system prompt missing, got %q", finalSys)
	}
	if !strings.Contains(finalSys, "@opencode-agent:simplifier") {
		t.Errorf("agent marker destroyed by rewriteSystemPrompt, got %q", finalSys)
	}
}

func TestRewriteSystemPrompt_PreservesMultipleMarkers(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "<!-- @opencode-agent:simplifier -->\n<!-- @opencode-agent:reviewer -->",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	result := rewriteSystemPrompt(body, "New system prompt.")

	var data map[string]interface{}
	json.Unmarshal(result, &data)
	sys := data["system"].(string)
	if !strings.Contains(sys, "@opencode-agent:simplifier") {
		t.Errorf("first marker lost, got %q", sys)
	}
	if !strings.Contains(sys, "@opencode-agent:reviewer") {
		t.Errorf("second marker lost, got %q", sys)
	}
	if !strings.Contains(sys, "New system prompt.") {
		t.Errorf("new prompt missing, got %q", sys)
	}
}

func TestRewriteSystemPrompt_NoMarker(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "Just a regular system prompt.",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	result := rewriteSystemPrompt(body, "Replaced prompt.")

	var data map[string]interface{}
	json.Unmarshal(result, &data)
	sys := data["system"].(string)
	if sys != "Replaced prompt." {
		t.Errorf("expected exact replacement, got %q", sys)
	}
}

func TestRewriteSystemPrompt_ListSystemPreservesMarker(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system": []map[string]string{
			{"type": "text", "text": "<!-- @opencode-agent:architect --> Some instructions"},
		},
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	result := rewriteSystemPrompt(body, "New prompt.")

	var data map[string]interface{}
	json.Unmarshal(result, &data)
	sys := data["system"].(string)
	if !strings.Contains(sys, "@opencode-agent:architect") {
		t.Errorf("marker from list system lost, got %q", sys)
	}
	if !strings.Contains(sys, "New prompt.") {
		t.Errorf("new prompt missing, got %q", sys)
	}
}

func TestSendLocalStub_NonStreaming(t *testing.T) {
	var buf bytes.Buffer
	sendLocalStub(&buf, "http://localhost:3456", false)

	output := buf.String()
	if !strings.Contains(output, "HTTP/1.1 200 OK") {
		t.Error("missing status line")
	}
	if !strings.Contains(output, "application/json") {
		t.Error("missing content type")
	}

	// Extract body after headers
	parts := strings.SplitN(output, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("could not split headers/body")
	}
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(parts[1]), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["type"] != "message" {
		t.Error("unexpected type")
	}
	if resp["model"] != "http://localhost:3456" {
		t.Error("unexpected model")
	}
	content := resp["content"].([]interface{})[0].(map[string]interface{})
	if !strings.Contains(content["text"].(string), "http://localhost:3456") {
		t.Error("stub text missing model name")
	}
	if !strings.Contains(content["text"].(string), "no local provider configured") {
		t.Error("stub text missing expected message")
	}
}

func TestSendLocalStub_Streaming(t *testing.T) {
	var buf bytes.Buffer
	sendLocalStub(&buf, "http://localhost:3456", true)

	output := buf.String()
	if !strings.Contains(output, "text/event-stream") {
		t.Error("missing SSE content type")
	}

	assertSSELifecycle(t, output)
	if !strings.Contains(output, "http://localhost:3456") {
		t.Error("missing model in SSE output")
	}
	if !strings.Contains(output, "no local provider configured") {
		t.Error("missing stub text in SSE output")
	}
}

func TestBypassList(t *testing.T) {
	for _, host := range []string{
		"pypi.org", "files.pythonhosted.org", "foo.pythonhosted.org",
		"registry.npmjs.org", "accounts.google.com",
		"github.com", "api.github.com", "proxy.golang.org",
		"crates.io", "sum.golang.org",
	} {
		if !shouldBypassMITM(host) {
			t.Errorf("shouldBypassMITM(%q) = false, want true", host)
		}
	}
	for _, host := range []string{
		"api.anthropic.com", "api.openai.com", "example.com",
	} {
		if shouldBypassMITM(host) {
			t.Errorf("shouldBypassMITM(%q) = true, want false", host)
		}
	}
}
