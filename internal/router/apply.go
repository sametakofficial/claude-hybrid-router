package router

import (
	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

// Apply adjusts a request for a provider route: the system prompt mode.
// (Model, max_tokens and temperature are applied by the provider adapter,
// since each wire format puts them differently.)
func Apply(req anthropic.Request, rt config.Route) {
	switch rt.SystemMode {
	case config.SystemReplace:
		if rt.SystemPrompt == "" {
			delete(req, "system") // an empty text block would be rejected
			return
		}
		req["system"] = []any{map[string]any{"type": "text", "text": rt.SystemPrompt}}
	case config.SystemAppend:
		if rt.SystemPrompt == "" {
			return
		}
		block := map[string]any{"type": "text", "text": rt.SystemPrompt}
		switch s := req["system"].(type) {
		case string:
			if s == "" {
				req["system"] = []any{block}
			} else {
				req["system"] = []any{map[string]any{"type": "text", "text": s}, block}
			}
		case []any:
			req["system"] = append(s, block)
		default:
			req["system"] = []any{block}
		}
	}
}
