// Package testutil provides helpers for proxy integration tests.
//
// MockMusistudioServer stands in for the real musistudio/claude-code-router
// egress: it accepts Anthropic-format POST /v1/messages requests and returns
// canned Anthropic-format responses (JSON or SSE), mirroring what the real
// server emits after its internal Anthropic↔OpenAI translation.
package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// MockMusistudioServer starts an Anthropic-compatible mock on a random port.
// It returns the server, port, and a getter for the last received request body
// (so tests can assert that model/max_tokens rewriting happened as expected).
func MockMusistudioServer() (srv *http.Server, port int, getLastBody func() []byte, getLastHeaders func() http.Header, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, nil, nil, err
	}
	port = ln.Addr().(*net.TCPAddr).Port

	var mu sync.Mutex
	var lastBody []byte
	var lastHeaders http.Header

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastBody = body
		lastHeaders = r.Header.Clone()
		mu.Unlock()

		var req struct {
			Model    string          `json:"model"`
			Messages json.RawMessage `json:"messages"`
			Stream   bool            `json:"stream"`
			Tools    json.RawMessage `json:"tools,omitempty"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		hasTools := len(req.Tools) > 0 && string(req.Tools) != "null"

		if req.Stream {
			writeAnthropicStream(w, req.Model, hasTools)
			return
		}
		writeAnthropicJSON(w, req.Model, hasTools)
	})

	srv = &http.Server{Handler: mux}
	go srv.Serve(ln)

	getLastBody = func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return lastBody
	}
	getLastHeaders = func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return lastHeaders
	}
	return srv, port, getLastBody, getLastHeaders, nil
}

func writeAnthropicJSON(w http.ResponseWriter, model string, hasTools bool) {
	var resp map[string]interface{}
	if hasTools {
		resp = map[string]interface{}{
			"id":    "msg_mock_tool",
			"type":  "message",
			"role":  "assistant",
			"model": model,
			"content": []map[string]interface{}{
				{
					"type":  "tool_use",
					"id":    "toolu_mock_123",
					"name":  "Read",
					"input": map[string]string{"file_path": "/tmp/test.txt"},
				},
			},
			"stop_reason":   "tool_use",
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": 100, "output_tokens": 20},
		}
	} else {
		resp = map[string]interface{}{
			"id":    "msg_mock_text",
			"type":  "message",
			"role":  "assistant",
			"model": model,
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": fmt.Sprintf("Mock response from %s", model),
				},
			},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": 100, "output_tokens": 20},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeAnthropicStream(w http.ResponseWriter, model string, hasTools bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	emit := func(event string, data interface{}) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	emit("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": "msg_mock_stream", "type": "message", "role": "assistant",
			"content": []interface{}{}, "model": model,
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 100, "output_tokens": 0},
		},
	})

	if hasTools {
		emit("content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]interface{}{
				"type": "tool_use", "id": "toolu_stream_1", "name": "Read", "input": map[string]string{},
			},
		})
		emit("content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]interface{}{
				"type":         "input_json_delta",
				"partial_json": `{"file_path":"/tmp/test.txt"}`,
			},
		})
		emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]interface{}{
			"type":  "message_delta",
			"delta": map[string]interface{}{"stop_reason": "tool_use", "stop_sequence": nil},
			"usage": map[string]int{"output_tokens": 20},
		})
	} else {
		emit("content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]interface{}{"type": "text", "text": ""},
		})
		for _, word := range strings.Fields(fmt.Sprintf("Mock streaming response from %s", model)) {
			emit("content_block_delta", map[string]interface{}{
				"type": "content_block_delta", "index": 0,
				"delta": map[string]interface{}{"type": "text_delta", "text": word + " "},
			})
		}
		emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]interface{}{
			"type":  "message_delta",
			"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]int{"output_tokens": 20},
		})
	}
	emit("message_stop", map[string]interface{}{"type": "message_stop"})
}
