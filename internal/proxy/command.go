package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
)

// forwardCommand handles requests for command bridge providers.
// Instead of translating to OpenAI and forwarding to an HTTP endpoint,
// it returns a tool_use response that makes Claude Code run a shell command.
//
// Turn 1 (no tool_result): Substitutes $AGENT/$PROMPT into the command
//
//	template and returns a tool_use response (Bash). For `opencode run`
//	bridges we additionally force --format json and, when available,
//	inject --session <id> so the subagent resumes its prior conversation
//	instead of starting fresh every turn.
//
// Turn 2 (has tool_result): For opencode runs we parse the JSON event
// stream to capture the newly emitted sessionID (cached for next time)
// and to extract just the assistant text. For other bridges we return
// the raw tool output verbatim.
func (p *Proxy) forwardCommand(w io.Writer, command, agentName, modelLabel string, body []byte, isStreaming bool) {
	opencode := isOpencodeRun(command)
	if hasToolResult(body) {
		toolOutput := extractToolResult(body)
		displayText := toolOutput
		if opencode {
			sessionID, text, errored := parseOpencodeOutput(toolOutput)
			failed := isOpencodeFailure(sessionID, text, toolOutput, errored)
			if failed {
				prior := defaultOpencodeSessions.Get(agentName)
				defaultOpencodeSessions.Forget(agentName)
				log.Printf("COMMAND_TURN2 %s → opencode failure detected, forgot session %q (text=%d chars, errored=%v)", modelLabel, prior, len(text), errored)
			} else if sessionID != "" {
				defaultOpencodeSessions.Set(agentName, sessionID)
				log.Printf("COMMAND_TURN2 %s → cached opencode session %s (text=%d chars)", modelLabel, sessionID, len(text))
			} else {
				log.Printf("COMMAND_TURN2 %s → no sessionID in opencode output (text=%d chars)", modelLabel, len(text))
			}
			if text != "" {
				displayText = text
			}
		} else {
			log.Printf("COMMAND_TURN2 %s → returning tool result as text (%d chars)", modelLabel, len(displayText))
		}
		resolved := config.ResolvedModel{Provider: modelLabel}
		if p.modelResolver != nil {
			if rm, err := p.modelResolver.Resolve(modelLabel); err == nil {
				resolved = rm
			}
		}
		displayText = maybeNormalizeExaToolResult(displayText, resolved)
		if isStreaming {
			writeSSEText(w, modelLabel, displayText)
		} else {
			writeJSONTextResponse(w, modelLabel, displayText)
		}
		return
	}

	prompt := extractPrompt(body)
	cmd := expandCommand(command, agentName, prompt)
	resumedFrom := ""
	if opencode {
		resumedFrom = defaultOpencodeSessions.Get(agentName)
		cmd = rewriteOpencodeCommand(cmd, resumedFrom)
	}
	if opencode {
		if resumedFrom != "" {
			log.Printf("COMMAND_TURN1 %s → opencode resume=%s Bash: %s", modelLabel, resumedFrom, truncate(cmd, 240))
		} else {
			log.Printf("COMMAND_TURN1 %s → opencode new session Bash: %s", modelLabel, truncate(cmd, 240))
		}
	} else {
		log.Printf("COMMAND_TURN1 %s → Bash: %s", modelLabel, truncate(cmd, 200))
	}
	if isStreaming {
		writeSSEToolUse(w, modelLabel, cmd)
	} else {
		writeJSONToolUse(w, modelLabel, cmd)
	}
}

// expandCommand substitutes template variables in the command string.
//
//	$AGENT  → resolved model value (e.g. agent name)
//	$PROMPT → extracted user prompt (single quotes escaped for shell)
func expandCommand(tmpl, agent, prompt string) string {
	escaped := strings.ReplaceAll(prompt, "'", "'\"'\"'")
	cmd := strings.ReplaceAll(tmpl, "$AGENT", agent)
	cmd = strings.ReplaceAll(cmd, "$PROMPT", escaped)
	return cmd
}

// extractPrompt gets the user message content, stripping system-reminder blocks.
func extractPrompt(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		var raw string
		switch c := msg.Content.(type) {
		case string:
			raw = c
		case []interface{}:
			var texts []string
			for _, block := range c {
				bm, ok := block.(map[string]interface{})
				if !ok {
					continue
				}
				if bm["type"] == "text" {
					if text, ok := bm["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
			raw = strings.Join(texts, "\n")
		}
		raw = stripSystemReminders(raw)
		raw = strings.TrimSpace(raw)
		if raw != "" {
			return raw
		}
	}
	return ""
}

// stripSystemReminders removes <system-reminder>...</system-reminder> blocks.
func stripSystemReminders(s string) string {
	for {
		start := strings.Index(s, "<system-reminder>")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "</system-reminder>")
		if end == -1 {
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</system-reminder>"):]
	}
	return s
}

// hasToolResult checks if messages contain a tool_result block.
func hasToolResult(body []byte) bool {
	var req struct {
		Messages []struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		contentArr, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, block := range contentArr {
			bm, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if bm["type"] == "tool_result" {
				return true
			}
		}
	}
	return false
}

// extractToolResult gets the content from the last tool_result block.
func extractToolResult(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	var result string
	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		contentArr, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, block := range contentArr {
			bm, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if bm["type"] != "tool_result" {
				continue
			}
			switch c := bm["content"].(type) {
			case string:
				result = c
			case []interface{}:
				var texts []string
				for _, cb := range c {
					cbm, ok := cb.(map[string]interface{})
					if !ok {
						continue
					}
					if text, ok := cbm["text"].(string); ok {
						texts = append(texts, text)
					}
				}
				result = strings.Join(texts, "\n")
			}
		}
	}
	return result
}

// --- Response writers ---

func generateID(prefix string) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 20)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return prefix + string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func writeJSONToolUse(w io.Writer, model, command string) {
	toolID := generateID("toolu_cmd_")
	resp := map[string]interface{}{
		"id":   generateID("msg_cmd_"),
		"type": "message",
		"role": "assistant",
		"content": []map[string]interface{}{
			{
				"type": "tool_use",
				"id":   toolID,
				"name": "Bash",
				"input": map[string]interface{}{
					"command":     command,
					"description": "Running command bridge",
					"timeout":     config.CommandBridgeTimeoutMS,
				},
			},
		},
		"model":         model,
		"stop_reason":   "tool_use",
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": 0, "output_tokens": 1},
	}
	respBody, _ := json.Marshal(resp)
	fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(respBody))
	w.Write(respBody)
}

func writeJSONTextResponse(w io.Writer, model, text string) {
	resp := map[string]interface{}{
		"id":   generateID("msg_cmd_"),
		"type": "message",
		"role": "assistant",
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
		"model":         model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": 0, "output_tokens": 1},
	}
	respBody, _ := json.Marshal(resp)
	fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(respBody))
	w.Write(respBody)
}

func writeSSEToolUse(w io.Writer, model, command string) {
	toolID := generateID("toolu_cmd_")
	msgID := generateID("msg_cmd_")

	inputJSON, _ := json.Marshal(map[string]interface{}{
		"command":     command,
		"description": "Running command bridge",
		"timeout":     config.CommandBridgeTimeoutMS,
	})

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
			"content_block": map[string]interface{}{
				"type": "tool_use", "id": toolID, "name": "Bash",
				"input": map[string]interface{}{},
			},
		}},
		{"content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]interface{}{
				"type":         "input_json_delta",
				"partial_json": string(inputJSON),
			},
		}},
		{"content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": 0,
		}},
		{"message_delta", map[string]interface{}{
			"type":  "message_delta",
			"delta": map[string]interface{}{"stop_reason": "tool_use", "stop_sequence": nil},
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

func writeSSEText(w io.Writer, model, text string) {
	msgID := generateID("msg_cmd_")

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
			"delta": map[string]string{"type": "text_delta", "text": text},
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
