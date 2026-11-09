package openaichat

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
)

// Usage is the token usage of a reply. InputTokens excludes CacheRead
// (Anthropic's convention).
type Usage struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int
}

// Wire converts to anthropic.Usage, whose Input includes cache reads.
func (u Usage) Wire() anthropic.Usage {
	return anthropic.Usage{Input: u.InputTokens + u.CacheRead, Output: u.OutputTokens, CacheRead: u.CacheRead}
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
		Name string `json:"name"`
		// Usually a string holding JSON; some servers send the object itself.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// argText returns the arguments as JSON text, whichever way they came.
func (tc oaToolCall) argText() string {
	raw := tc.Function.Arguments
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

// oaMessage is a full message or a stream delta.
type oaMessage struct {
	Content          any          `json:"content"`
	ReasoningContent any          `json:"reasoning_content"` // DeepSeek, Kimi, GLM, vLLM
	Reasoning        any          `json:"reasoning"`         // OpenRouter
	ToolCalls        []oaToolCall `json:"tool_calls"`
}

// reasoning returns reasoning text and the field it came in.
func (m oaMessage) reasoning() (text, field string) {
	if t, ok := m.ReasoningContent.(string); ok && t != "" {
		return t, "reasoning_content"
	}
	if t, ok := m.Reasoning.(string); ok && t != "" {
		return t, "reasoning"
	}
	return "", ""
}

type oaResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      oaMessage `json:"message"`
		Delta        oaMessage `json:"delta"`
		FinishReason *string   `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage        `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// errorText reads an error given as a string or as {"message": ...}.
func errorText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	return string(raw)
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
	if json.Valid([]byte(args)) && strings.HasPrefix(args, "{") {
		return json.RawMessage(args)
	}
	// Keep broken arguments visible instead of failing the whole turn.
	b, _ := json.Marshal(map[string]string{"_raw_arguments": args})
	return b
}

// newID returns a unique id such as toolu_9f2c... (providers may send no
// tool call id, or the same one every turn).
func newID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func msgID(id string) string {
	if id == "" {
		return newID("msg_")
	}
	return "msg_" + strings.TrimPrefix(id, "chatcmpl-")
}

func signature(field string) string { return anthropic.SignaturePrefix + field }

// FromOpenAI converts a non-streaming Chat Completions response into an
// Anthropic Messages response.
func FromOpenAI(body []byte, model string) ([]byte, Usage, error) {
	var r oaResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, Usage{}, fmt.Errorf("decode response: %w", err)
	}
	if msg := errorText(r.Error); msg != "" {
		return nil, Usage{}, fmt.Errorf("provider error: %s", msg)
	}
	if len(r.Choices) == 0 {
		return nil, Usage{}, fmt.Errorf("provider returned no choices")
	}
	ch := r.Choices[0]
	content := []map[string]any{}
	if t, field := ch.Message.reasoning(); t != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": t, "signature": signature(field)})
	}
	if t := textOf(ch.Message.Content); t != "" {
		content = append(content, map[string]any{"type": "text", "text": t})
	}
	for _, tc := range ch.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = newID("toolu_")
		}
		content = append(content, map[string]any{
			"type": "tool_use", "id": id, "name": tc.Function.Name, "input": toolInput(tc.argText()),
		})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	finish := ""
	if ch.FinishReason != nil {
		finish = *ch.FinishReason
	}
	usage := r.Usage.toUsage()
	out, err := json.Marshal(map[string]any{
		"id": msgID(r.ID), "type": "message", "role": "assistant", "model": model,
		"content":       content,
		"stop_reason":   stopReason(finish, len(ch.Message.ToolCalls) > 0),
		"stop_sequence": nil,
		"usage":         usage.anthropic(),
	})
	return out, usage, err
}

// Stream converts an OpenAI reply into an Anthropic SSE stream.
//
// Reasoning text (reasoning_content / reasoning) becomes a thinking block,
// so Claude Code shows progress while a reasoning model thinks. The block
// carries a placeholder signature (anthropic.SignaturePrefix + field);
// ToOpenAI turns it back into the same field, and it is removed before a
// request goes to Anthropic.
//
// Text is streamed as it arrives. Tool calls are collected and sent as whole
// blocks at the end, which keeps the block order valid even when a provider
// interleaves deltas for several tool calls.
type Stream struct {
	sse        *anthropic.SSE
	model      string
	inputGuess int // input_tokens for message_start; the real count follows in message_delta

	started   bool
	index     int    // next Anthropic content block index
	open      string // "", "text" or "thinking": the block at index-1
	thinkFrom string // field the open thinking block came from
	tools     []*oaToolCall
	byIndex   map[int]*oaToolCall
	finish    string
	usage     Usage
}

// NewStream creates a converter writing to w; flush is called after each
// event. inputGuess is reported as input_tokens in message_start (the
// provider's real count only arrives at the end).
func NewStream(w io.Writer, flush func(), model string, inputGuess int) *Stream {
	return &Stream{sse: anthropic.NewSSE(w, flush), model: model, inputGuess: inputGuess, byIndex: map[int]*oaToolCall{}}
}

func (s *Stream) event(name string, data map[string]any) error { return s.sse.Event(name, data) }

func (s *Stream) start(id string) error {
	if s.started {
		return nil
	}
	s.started = true
	return s.event("message_start", anthropic.MessageStart(msgID(id), s.model, s.inputGuess))
}

func (s *Stream) openBlock(kind string, block map[string]any) error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	if err := s.event("content_block_start", map[string]any{"type": "content_block_start", "index": s.index, "content_block": block}); err != nil {
		return err
	}
	s.open = kind
	s.index++
	return nil
}

func (s *Stream) closeBlock() error {
	switch s.open {
	case "":
		return nil
	case "thinking":
		if err := s.event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.index - 1,
			"delta": map[string]any{"type": "signature_delta", "signature": signature(s.thinkFrom)},
		}); err != nil {
			return err
		}
	}
	s.open = ""
	return s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index - 1})
}

func (s *Stream) thinking(t, field string) error {
	if s.open != "thinking" || s.thinkFrom != field {
		if err := s.openBlock("thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""}); err != nil {
			return err
		}
		s.thinkFrom = field
	}
	return s.event("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": s.index - 1,
		"delta": map[string]any{"type": "thinking_delta", "thinking": t},
	})
}

func (s *Stream) text(t string) error {
	if s.open != "text" {
		if err := s.openBlock("text", map[string]any{"type": "text", "text": ""}); err != nil {
			return err
		}
	}
	return s.event("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": s.index - 1,
		"delta": map[string]any{"type": "text_delta", "text": t},
	})
}

func (s *Stream) toolDelta(tc oaToolCall) {
	cur, ok := s.byIndex[tc.Index]
	// A new id at a known index means the provider did not set indexes
	// (seen with some servers): treat it as a new call.
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
	if args := tc.argText(); args != "" {
		prev := ""
		if len(cur.Function.Arguments) > 0 {
			json.Unmarshal(cur.Function.Arguments, &prev)
		}
		cur.Function.Arguments, _ = json.Marshal(prev + args)
	}
}

// message handles one delta (or a whole message).
func (s *Stream) message(m oaMessage) error {
	if t, field := m.reasoning(); t != "" {
		if err := s.thinking(t, field); err != nil {
			return err
		}
	}
	if t := textOf(m.Content); t != "" {
		if err := s.text(t); err != nil {
			return err
		}
	}
	for _, tc := range m.ToolCalls {
		s.toolDelta(tc)
	}
	return nil
}

// chunk handles one parsed stream chunk or full response.
func (s *Stream) chunk(c oaResponse, full bool) error {
	if msg := errorText(c.Error); msg != "" {
		return s.fail("provider error: " + msg)
	}
	if err := s.start(c.ID); err != nil {
		return err
	}
	if c.Usage != nil {
		s.usage = c.Usage.toUsage()
	}
	for _, ch := range c.Choices {
		m := ch.Delta
		if full {
			m = ch.Message
		}
		if err := s.message(m); err != nil {
			return err
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			s.finish = *ch.FinishReason
		}
	}
	return nil
}

// Run reads an OpenAI SSE stream from r and writes Anthropic events.
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
		var c oaResponse
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue // skip keep-alives and junk lines
		}
		if err := s.chunk(c, false); err != nil {
			return s.usage, err
		}
	}
	if err := sc.Err(); err != nil {
		return s.usage, s.fail("stream read error: " + err.Error())
	}
	if !done && s.finish == "" {
		// The connection ended without finish_reason or [DONE]: the answer
		// is cut off. Reporting end_turn would make it look complete.
		return s.usage, s.fail("provider closed the stream before the response was complete")
	}
	return s.usage, s.finishStream()
}

// RunJSON writes a non-streaming Chat Completions reply (from a provider
// that ignored stream=true) as an Anthropic stream.
func (s *Stream) RunJSON(data []byte) (Usage, error) {
	var c oaResponse
	if err := json.Unmarshal(data, &c); err != nil {
		return s.usage, s.fail(fmt.Sprintf("provider sent an unreadable reply: %.300s", strings.TrimSpace(string(data))))
	}
	if msg := errorText(c.Error); msg != "" {
		return s.usage, s.fail("provider error: " + msg)
	}
	if len(c.Choices) == 0 {
		return s.usage, s.fail("provider returned no choices")
	}
	if err := s.chunk(c, true); err != nil {
		return s.usage, err
	}
	return s.usage, s.finishStream()
}

func (s *Stream) finishStream() error {
	if err := s.start(""); err != nil {
		return err
	}
	if err := s.closeBlock(); err != nil {
		return err
	}
	for _, tc := range s.tools {
		id := tc.ID
		if id == "" {
			id = newID("toolu_")
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
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(toolInput(tc.argText()))},
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
		if err := s.closeBlock(); err != nil {
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
	_ = s.sse.Error("api_error", msg)
	return fmt.Errorf("%s", msg)
}
