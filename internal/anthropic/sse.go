package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SSE writes Anthropic server-sent events, flushing after each one.
type SSE struct {
	w     io.Writer
	flush func()
}

// NewSSE writes events to w; flush (optional) runs after every event.
func NewSSE(w io.Writer, flush func()) *SSE {
	if flush == nil {
		flush = func() {}
	}
	return &SSE{w: w, flush: flush}
}

// StartSSE sets the stream headers, sends 200 and returns an SSE writer
// that flushes w after every event.
func StartSSE(w http.ResponseWriter) *SSE {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return NewSSE(w, func() {
		if f != nil {
			f.Flush()
		}
	})
}

// Event sends one event; data is JSON-encoded.
func (s *SSE) Event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	s.flush()
	return nil
}

// Error sends an error event (how a stream reports failure after 200).
func (s *SSE) Error(typ, msg string) error {
	return s.Event("error", map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
}

// Ping sends a ping event, which keeps a quiet stream alive.
func (s *SSE) Ping() error { return s.Event("ping", map[string]any{"type": "ping"}) }

// MessageStart is the data of a message_start event.
func MessageStart(id, model string, inputTokens int) map[string]any {
	return map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
		},
	}
}
