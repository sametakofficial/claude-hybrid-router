// Package translate converts between the Anthropic Messages API and the
// OpenAI Chat Completions API.
package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Options adjusts the translated request.
type Options struct {
	Model           string   // target model; "" keeps the request's model
	MaxTokensCap    int      // 0 = no cap
	MaxTokensField  string   // "max_tokens" (default) or "max_completion_tokens"
	Temperature     *float64 // nil keeps the request's temperature
	ReasoningEffort string   // "" = not sent
}

type anthropicRequest struct {
	Model         string          `json:"model"`
	System        json.RawMessage `json:"system"`
	Messages      []message       `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	StopSequences []string        `json:"stop_sequences"`
	Stream        bool            `json:"stream"`
	Tools         []tool          `json:"tools"`
	ToolChoice    *toolChoice     `json:"tool_choice"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Source    *source         `json:"source"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type source struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

type tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
}

// parseBlocks reads content that may be a plain string or a block list.
func parseBlocks(raw json.RawMessage) ([]block, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return []block{{Type: "text", Text: s}}, nil
	}
	var bs []block
	err := json.Unmarshal(raw, &bs)
	return bs, err
}

// SystemText flattens an Anthropic system field (string or blocks) to text.
func SystemText(raw json.RawMessage) string {
	bs, err := parseBlocks(raw)
	if err != nil {
		return ""
	}
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func imagePart(s *source) map[string]any {
	if s == nil {
		return nil
	}
	url := s.URL
	if s.Type == "base64" {
		url = "data:" + s.MediaType + ";base64," + s.Data
	}
	if url == "" {
		return nil
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

// contentValue returns a plain string when there are only text parts, since
// many OpenAI-compatible servers do not accept part arrays.
func contentValue(parts []map[string]any) any {
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p["type"] != "text" {
			return parts
		}
		texts = append(texts, p["text"].(string))
	}
	return strings.Join(texts, "\n\n")
}

// toolResultText flattens a tool_result's content; images are returned separately.
func toolResultText(raw json.RawMessage) (string, []map[string]any) {
	bs, err := parseBlocks(raw)
	if err != nil {
		return string(raw), nil
	}
	var texts []string
	var images []map[string]any
	for _, b := range bs {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "image":
			if p := imagePart(b.Source); p != nil {
				images = append(images, p)
			}
		}
	}
	return strings.Join(texts, "\n"), images
}

// ToOpenAI converts an Anthropic Messages request body into an OpenAI Chat
// Completions request body.
func ToOpenAI(body []byte, opt Options) ([]byte, error) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}

	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if sys := SystemText(req.System); sys != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sys})
	}

	for i, m := range req.Messages {
		bs, err := parseBlocks(m.Content)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		switch m.Role {
		case "assistant":
			var texts []string
			var calls []map[string]any
			for _, b := range bs {
				switch b.Type {
				case "text":
					if b.Text != "" {
						texts = append(texts, b.Text)
					}
				case "tool_use", "server_tool_use":
					args := string(b.Input)
					if args == "" || args == "null" {
						args = "{}"
					}
					calls = append(calls, map[string]any{
						"id": b.ID, "type": "function",
						"function": map[string]any{"name": b.Name, "arguments": args},
					})
				}
				// thinking / redacted_thinking blocks have no OpenAI equivalent.
			}
			if len(texts) == 0 && len(calls) == 0 {
				continue
			}
			am := map[string]any{"role": "assistant", "content": strings.Join(texts, "\n\n")}
			if len(calls) > 0 {
				am["tool_calls"] = calls
			}
			msgs = append(msgs, am)

		default: // user
			var parts []map[string]any
			var extraImages []map[string]any
			for _, b := range bs {
				switch b.Type {
				case "tool_result":
					text, imgs := toolResultText(b.Content)
					if b.IsError {
						text = "[tool error] " + text
					}
					if text == "" {
						text = "(no output)"
					}
					msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": b.ToolUseID, "content": text})
					extraImages = append(extraImages, imgs...)
				case "text":
					if b.Text != "" {
						parts = append(parts, map[string]any{"type": "text", "text": b.Text})
					}
				case "image":
					if p := imagePart(b.Source); p != nil {
						parts = append(parts, p)
					}
				case "document":
					if b.Source != nil && b.Source.Type == "text" {
						parts = append(parts, map[string]any{"type": "text", "text": b.Source.Data})
					} else {
						parts = append(parts, map[string]any{"type": "text", "text": "[document omitted: not supported by this provider]"})
					}
				}
			}
			// Images from tool results cannot live in a "tool" message, so
			// they follow in a user message.
			if len(extraImages) > 0 {
				parts = append([]map[string]any{{"type": "text", "text": "Images returned by the tool:"}}, append(extraImages, parts...)...)
			}
			if len(parts) > 0 {
				msgs = append(msgs, map[string]any{"role": "user", "content": contentValue(parts)})
			}
		}
	}

	out := map[string]any{"model": req.Model, "messages": msgs}
	if opt.Model != "" {
		out["model"] = opt.Model
	}

	maxTokens := req.MaxTokens
	if opt.MaxTokensCap > 0 && (maxTokens == 0 || maxTokens > opt.MaxTokensCap) {
		maxTokens = opt.MaxTokensCap
	}
	if maxTokens > 0 {
		field := opt.MaxTokensField
		if field == "" {
			field = "max_tokens"
		}
		out[field] = maxTokens
	}
	if opt.Temperature != nil {
		out["temperature"] = *opt.Temperature
	} else if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		out["stop"] = req.StopSequences
	}
	if opt.ReasoningEffort != "" {
		out["reasoning_effort"] = opt.ReasoningEffort
	}
	if req.Stream {
		out["stream"] = true
		out["stream_options"] = map[string]any{"include_usage": true}
	}

	var tools []map[string]any
	for _, t := range req.Tools {
		// Server tools (web_search, etc.) have no input_schema and cannot run elsewhere.
		if len(t.InputSchema) == 0 || (t.Type != "" && t.Type != "custom") {
			continue
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": cleanSchema(t.InputSchema),
			},
		})
	}
	if len(tools) > 0 {
		out["tools"] = tools
		if tc := req.ToolChoice; tc != nil {
			switch tc.Type {
			case "any":
				out["tool_choice"] = "required"
			case "none":
				out["tool_choice"] = "none"
			case "tool":
				out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
			}
			if tc.DisableParallelToolUse {
				out["parallel_tool_calls"] = false
			}
		}
	}
	return json.Marshal(out)
}

// cleanSchema drops JSON Schema keys that some OpenAI-compatible servers reject.
func cleanSchema(raw json.RawMessage) any {
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	delete(v, "$schema")
	if _, ok := v["type"]; !ok {
		v["type"] = "object"
	}
	return v
}
