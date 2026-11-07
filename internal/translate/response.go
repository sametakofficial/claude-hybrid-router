package translate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Usage is the token usage reported back to the caller.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int
}

type oaUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u *oaUsage) toUsage() Usage {
	if u == nil {
		return Usage{}
	}
	out := Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	if u.PromptTokensDetails != nil {
		out.CacheRead = u.PromptTokensDetails.CachedTokens
		out.InputTokens -= out.CacheRead
	}
	return out
}

func (u Usage) anthropic() map[string]any {
	m := map[string]any{"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens}
	if u.CacheRead > 0 {
		m["cache_read_input_tokens"] = u.CacheRead
	}
	return m
}

type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content   any          `json:"content"`
			ToolCalls []oaToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func stopReason(finish string, hasTools bool) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	}
	if hasTools {
		return "tool_use"
	}
	return "end_turn"
}

// textOf handles content as a string or as an array of {type,text} parts.
func textOf(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, p := range c {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

func toolInput(args string) json.RawMessage {
	args = strings.TrimSpace(args)
	if args == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(args)) {
		return json.RawMessage(args)
	}
	// Keep broken arguments visible instead of failing the whole turn.
	b, _ := json.Marshal(map[string]string{"_raw_arguments": args})
	return b
}

func msgID(id string) string {
	if id == "" {
		return "msg_chr"
	}
	return "msg_" + strings.TrimPrefix(id, "chatcmpl-")
}

// FromOpenAI converts a non-streaming Chat Completions response into an
// Anthropic Messages response.
func FromOpenAI(body []byte, model string) ([]byte, Usage, error) {
	var r oaResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, Usage{}, fmt.Errorf("decode response: %w", err)
	}
	if r.Error != nil {
		return nil, Usage{}, fmt.Errorf("provider error: %s", r.Error.Message)
	}
	if len(r.Choices) == 0 {
		return nil, Usage{}, fmt.Errorf("provider returned no choices")
	}
	ch := r.Choices[0]
	content := []map[string]any{}
	if t := textOf(ch.Message.Content); t != "" {
		content = append(content, map[string]any{"type": "text", "text": t})
	}
	for _, tc := range ch.Message.ToolCalls {
		content = append(content, map[string]any{
			"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": toolInput(tc.Function.Arguments),
		})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	usage := r.Usage.toUsage()
	out, err := json.Marshal(map[string]any{
		"id": msgID(r.ID), "type": "message", "role": "assistant", "model": model,
		"content":       content,
		"stop_reason":   stopReason(ch.FinishReason, len(ch.Message.ToolCalls) > 0),
		"stop_sequence": nil,
		"usage":         usage.anthropic(),
	})
	return out, usage, err
}

type oaChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content   any          `json:"content"`
			ToolCalls []oaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream converts an OpenAI SSE stream into an Anthropic SSE stream.
//
// Text is streamed as it arrives. Tool calls are collected and sent as whole
// blocks at the end, which keeps the block order valid even when a provider
// interleaves deltas for several tool calls.
type Stream struct {
	w     io.Writer
	flush func()
	model string

	started  bool
	index    int  // next Anthropic content block index
	textOpen bool // a text block is open at index-1
	tools    []*oaToolCall
	byIndex  map[int]*oaToolCall
	finish   string
	usage    Usage
	gotAny   bool
}

// NewStream creates a converter writing to w; flush is called after each event.
func NewStream(w io.Writer, flush func(), model string) *Stream {
	if flush == nil {
		flush = func() {}
	}
	return &Stream{w: w, flush: flush, model: model, byIndex: map[int]*oaToolCall{}}
}

func (s *Stream) event(name string, data any) error {
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

func (s *Stream) start(id string) error {
	if s.started {
		return nil
	}
	s.started = true
	return s.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID(id), "type": "message", "role": "assistant", "model": s.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (s *Stream) text(t string) error {
	if !s.textOpen {
		if err := s.event("content_block_start", map[string]any{
			"type": "content_block_start", "index": s.index,
			"content_block": map[string]any{"type": "text", "text": ""},
		}); err != nil {
			return err
		}
		s.textOpen = true
		s.index++
	}
	return s.event("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": s.index - 1,
		"delta": map[string]any{"type": "text_delta", "text": t},
	})
}

func (s *Stream) closeText() error {
	if !s.textOpen {
		return nil
	}
	s.textOpen = false
	return s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index - 1})
}

// Run reads the whole OpenAI stream from r and writes Anthropic events.
// It returns the usage and an error if the stream failed.
func (s *Stream) Run(r io.Reader) (Usage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	done := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var c oaChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue // skip keep-alives and junk lines
		}
		if c.Error != nil {
			return s.usage, s.fail("provider error: " + c.Error.Message)
		}
		if err := s.start(c.ID); err != nil {
			return s.usage, err
		}
		if c.Usage != nil {
			s.usage = c.Usage.toUsage()
		}
		for _, ch := range c.Choices {
			if t := textOf(ch.Delta.Content); t != "" {
				s.gotAny = true
				if err := s.text(t); err != nil {
					return s.usage, err
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				s.gotAny = true
				cur, ok := s.byIndex[tc.Index]
				// A new id at a known index means the provider did not set
				// indexes (seen with some servers): treat it as a new call.
				if ok && tc.ID != "" && cur.ID != "" && tc.ID != cur.ID {
					ok = false
				}
				if !ok {
					cur = &oaToolCall{Index: tc.Index}
					s.byIndex[tc.Index] = cur
					s.tools = append(s.tools, cur)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				cur.Function.Name += tc.Function.Name
				cur.Function.Arguments += tc.Function.Arguments
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				s.finish = *ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return s.usage, s.fail("stream read error: " + err.Error())
	}
	if !done && s.finish == "" && !s.gotAny {
		return s.usage, s.fail("provider closed the stream without a response")
	}
	return s.usage, s.finishStream()
}

func (s *Stream) finishStream() error {
	if err := s.start(""); err != nil {
		return err
	}
	if err := s.closeText(); err != nil {
		return err
	}
	for i, tc := range s.tools {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("toolu_chr_%d", i)
		}
		idx := s.index
		s.index++
		if err := s.event("content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": map[string]any{"type": "tool_use", "id": id, "name": tc.Function.Name, "input": map[string]any{}},
		}); err != nil {
			return err
		}
		if err := s.event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(toolInput(tc.Function.Arguments))},
		}); err != nil {
			return err
		}
		if err := s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx}); err != nil {
			return err
		}
	}
	if s.index == 0 { // Anthropic responses always carry at least one block.
		if err := s.text(""); err != nil {
			return err
		}
		if err := s.closeText(); err != nil {
			return err
		}
	}
	if err := s.event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason(s.finish, len(s.tools) > 0), "stop_sequence": nil},
		"usage": s.usage.anthropic(),
	}); err != nil {
		return err
	}
	return s.event("message_stop", map[string]any{"type": "message_stop"})
}

// fail sends an Anthropic error event so the client sees why the turn ended.
func (s *Stream) fail(msg string) error {
	_ = s.event("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": msg},
	})
	return fmt.Errorf("%s", msg)
}
