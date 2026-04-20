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
	if route.Agent != "" {
		t.Fatalf("expected empty agent, got %q", route.Agent)
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

func TestDetectLocalRoute_MarkerInMessages(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{{
			"role":    "user",
			"content": "<!-- @proxy-local-route:af83e9 url=http://localhost:3456 --> hello",
		}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "" {
		t.Fatalf("should not detect marker in messages, got %q", route.Route)
	}
	if !bytes.Equal(stripped, body) {
		t.Error("body should be unchanged")
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

func TestDetectLocalRoute_WithAgent(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "<!-- @proxy-local-route:af83e9 url=http://localhost:4567 agent=simplifier --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "http://localhost:4567" {
		t.Fatalf("expected url=http://localhost:4567, got %q", route.Route)
	}
	if route.Agent != "simplifier" {
		t.Fatalf("expected agent=simplifier, got %q", route.Agent)
	}

	var data map[string]interface{}
	json.Unmarshal(stripped, &data)
	sys := data["system"].(string)
	if sys != "You are helpful" {
		t.Errorf("expected stripped system, got %q", sys)
	}
}

func TestDetectLocalRoute_WithAgentListSystem(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system": []map[string]string{
			{"type": "text", "text": "<!-- @proxy-local-route:af83e9 url=http://localhost:4567 agent=reviewer --> Instructions"},
		},
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	route, stripped := detectLocalRoute(body)
	if route.Route != "http://localhost:4567" {
		t.Fatalf("expected url=http://localhost:4567, got %q", route.Route)
	}
	if route.Agent != "reviewer" {
		t.Fatalf("expected agent=reviewer, got %q", route.Agent)
	}

	var data map[string]interface{}
	json.Unmarshal(stripped, &data)
	blocks := data["system"].([]interface{})
	text := blocks[0].(map[string]interface{})["text"].(string)
	if text != "Instructions" {
		t.Errorf("expected stripped text, got %q", text)
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
