// Package anthropic holds helpers for the Anthropic Messages wire format
// shared by the proxy, the provider adapters and the opencode bridge.
package anthropic

import (
	"bytes"
	"encoding/json"
)

// Request is a parsed Messages request body. Numbers stay exact
// (json.Number), so re-encoding does not change them.
type Request map[string]any

// ParseRequest decodes a request body.
func ParseRequest(body []byte) (Request, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var r Request
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	return r, nil
}

// Model is the requested model ("" if missing).
func (r Request) Model() string { s, _ := r["model"].(string); return s }

// Stream reports whether a streaming reply was asked for.
func (r Request) Stream() bool { b, _ := r["stream"].(bool); return b }

// EditSystem calls fn for every text in the system field (a string or text
// blocks) and stores what fn returns.
func (r Request) EditSystem(fn func(string) string) {
	switch s := r["system"].(type) {
	case string:
		r["system"] = fn(s)
	case []any:
		for _, b := range s {
			if m, ok := b.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					m["text"] = fn(t)
				}
			}
		}
	}
}

// Int reads an integer field (json.Number or float64); ok is false if it is
// missing or not a number.
func (r Request) Int(key string) (int, bool) {
	switch v := r[key].(type) {
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}
