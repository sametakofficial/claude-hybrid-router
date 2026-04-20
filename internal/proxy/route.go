package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Matches both HTML-comment-wrapped and bare forms so the marker survives
// markdown parsers that strip HTML comments (e.g. Claude Code's CLAUDE.md/agent loader).
//
// The proxy is a pure URL forwarder — it extracts only the URL. Any service
// behind that URL (e.g. opencode-bridge) is responsible for parsing its own
// metadata (agent name, etc.) from the request body.
var routeMarkerRE = regexp.MustCompile(`(?:<!--\s*)?@proxy-local-route:af83e9\s+url=(\S+?)(?:\s*-->)?(?:\s|$)`)

// RouteDirective holds the parsed fields from a routing marker.
type RouteDirective struct {
	Route string // destination base URL (e.g. "http://127.0.0.1:4568")
}

// detectLocalRoute checks the system field of a JSON body for a routing marker.
// Returns a RouteDirective and the body with the marker stripped, or a zero-value
// RouteDirective and the original body.
func detectLocalRoute(body []byte) (route RouteDirective, stripped []byte) {
	if len(body) == 0 {
		return RouteDirective{}, body
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return RouteDirective{}, body
	}

	// Check system field first (traditional placement).
	if system, ok := data["system"]; ok && system != nil {
		if route, stripped, found := scanSystemField(data, system); found {
			return route, stripped
		}
	}

	// Claude Code packages CLAUDE.md and agent markdown as a USER message
	// with <system-reminder> blocks, so the marker lands in
	// messages[].content[].text rather than the top-level system field.
	// Scan every user message's text blocks as a fallback.
	if messages, ok := data["messages"].([]interface{}); ok {
		for _, msg := range messages {
			mm, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			if role, _ := mm["role"].(string); role != "user" {
				continue
			}
			content := mm["content"]
			switch c := content.(type) {
			case string:
				if m := routeMarkerRE.FindStringSubmatch(c); m != nil {
					mm["content"] = strings.TrimSpace(routeMarkerRE.ReplaceAllString(c, ""))
					out, _ := json.Marshal(data)
					return RouteDirective{Route: m[1]}, out
				}
			case []interface{}:
				for _, block := range c {
					bm, ok := block.(map[string]interface{})
					if !ok {
						continue
					}
					text, ok := bm["text"].(string)
					if !ok {
						continue
					}
					if m := routeMarkerRE.FindStringSubmatch(text); m != nil {
						bm["text"] = strings.TrimSpace(routeMarkerRE.ReplaceAllString(text, ""))
						out, _ := json.Marshal(data)
						return RouteDirective{Route: m[1]}, out
					}
				}
			}
		}
	}

	return RouteDirective{}, body
}

// scanSystemField checks the system field (string or block array) for the
// routing marker. If found, strips the marker and returns the re-marshaled body.
func scanSystemField(data map[string]interface{}, system interface{}) (RouteDirective, []byte, bool) {
	switch s := system.(type) {
	case string:
		if m := routeMarkerRE.FindStringSubmatch(s); m != nil {
			data["system"] = strings.TrimSpace(routeMarkerRE.ReplaceAllString(s, ""))
			out, _ := json.Marshal(data)
			return RouteDirective{Route: m[1]}, out, true
		}
	case []interface{}:
		for _, block := range s {
			bm, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			text, ok := bm["text"].(string)
			if !ok {
				continue
			}
			if m := routeMarkerRE.FindStringSubmatch(text); m != nil {
				bm["text"] = strings.TrimSpace(routeMarkerRE.ReplaceAllString(text, ""))
				out, _ := json.Marshal(data)
				return RouteDirective{Route: m[1]}, out, true
			}
		}
	}
	return RouteDirective{}, nil, false
}


// sendLocalStub writes an Anthropic Messages API stub response.
func sendLocalStub(w io.Writer, model string, streaming bool) {
	stubText := fmt.Sprintf("[Local model '%s' request intercepted by proxy — no local provider configured yet]", model)
	msgID := "msg_stub_local_route"

	if streaming {
		writeSSEStub(w, msgID, model, stubText)
	} else {
		writeJSONStub(w, msgID, model, stubText)
	}
}

func writeJSONStub(w io.Writer, msgID, model, stubText string) {
	resp := map[string]interface{}{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"content":       []map[string]string{{"type": "text", "text": stubText}},
		"model":         model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": 0, "output_tokens": 1},
	}
	respBody, _ := json.Marshal(resp)
	fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(respBody))
	w.Write(respBody)
}

func writeSSEStub(w io.Writer, msgID, model, stubText string) {
	events := []struct {
		event string
		data  interface{}
	}{
		{"message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id": msgID, "type": "message", "role": "assistant",
				"content": []interface{}{}, "model": model,
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
			},
		}},
		{"content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]string{"type": "text", "text": ""},
		}},
		{"content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]string{"type": "text_delta", "text": stubText},
		}},
		{"content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": 0,
		}},
		{"message_delta", map[string]interface{}{
			"type":  "message_delta",
			"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]int{"output_tokens": 1},
		}},
		{"message_stop", map[string]interface{}{
			"type": "message_stop",
		}},
	}

	var sseBody []byte
	for _, ev := range events {
		data, _ := json.Marshal(ev.data)
		sseBody = append(sseBody, []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", ev.event, data))...)
	}

	fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(sseBody))
	w.Write(sseBody)
}
