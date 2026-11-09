// opencode-bridge: Anthropic-API-compatible server that mimics a real
// assistant by returning a tool_use(Bash) block running `opencode run ...`.
// Claude Code renders the tool call, executes it, and sends back stdout as
// tool_result in turn 2 — at which point the bridge parses opencode's
// newline-delimited JSON events and replies with the assistant text.
//
// Agent-name detection is done from the incoming request body alone (no
// header magic, no marker carried by the proxy). The bridge looks for a
// `<!-- @opencode-agent:NAME -->` marker anywhere in system/messages; if
// none found, falls back to `general`. Session IDs are cached per-agent in
// a JSON file so repeated invocations reuse opencode's context.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

const (
	defaultAgent = "general"
	sessionTTL   = 2 * time.Hour
	maxBody      = 16 << 20
)

// bridge holds the server state.
type bridge struct {
	sessions *sessionCache
}

func main() {
	port := flag.Int("port", 4568, "port to listen on (Anthropic-compatible)")
	cachePath := flag.String("cache", defaultCachePath(),
		"JSON file for disk-persisted opencode session cache")
	flag.Parse()

	b := &bridge{sessions: newSessionCache(*cachePath, sessionTTL)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", b.handleMessages)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("opencode-bridge listening on %s (cache=%s)", addr, *cachePath)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// defaultCachePath lives in the claude-hybrid home ($CLAUDE_HYBRID_HOME or
// ~/.claude-hybrid).
func defaultCachePath() string {
	return filepath.Join(config.Home(), "opencode-sessions.json")
}

// --- Anthropic request handling ---

type anthropicRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	System   json.RawMessage `json:"system"`
	Messages []message       `json:"messages"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (b *bridge) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	r.Body.Close()
	if err != nil {
		anthropic.WriteErrorType(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	var req anthropicRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		anthropic.WriteErrorType(w, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	agent := detectAgent(req)
	modelLabel := "opencode/" + agent

	// Turn 2 check: does the body contain a tool_result we should parse?
	if toolOut, ok := extractLatestToolResult(req.Messages); ok {
		sessionID, text := parseOpencodeOutput(toolOut)
		if sessionID != "" {
			b.sessions.Set(agent, sessionID)
			log.Printf("TURN2 agent=%s → cached session %s (text=%d chars)",
				agent, sessionID, len(text))
		} else {
			log.Printf("TURN2 agent=%s → no sessionID in tool output (text=%d chars)",
				agent, len(text))
		}
		if text == "" {
			text = toolOut
		}
		if req.Stream {
			writeSSEText(w, modelLabel, text)
		} else {
			writeJSONText(w, modelLabel, text)
		}
		return
	}

	// Turn 1: build the opencode command and return as tool_use(Bash).
	prompt := extractPrompt(req.Messages)
	if prompt == "" {
		anthropic.WriteErrorType(w, http.StatusBadRequest, "invalid_request_error", "No user message found")
		return
	}

	resumed := b.sessions.Get(agent)
	cmd := buildOpencodeCommand(agent, prompt, resumed)
	if resumed != "" {
		log.Printf("TURN1 agent=%s → resume=%s bash=%s", agent, resumed, truncate(cmd, 200))
	} else {
		log.Printf("TURN1 agent=%s → new session bash=%s", agent, truncate(cmd, 160))
	}

	if req.Stream {
		writeSSEToolUse(w, modelLabel, cmd)
	} else {
		writeJSONToolUse(w, modelLabel, cmd)
	}
}

// --- Agent-name detection ---

// agentMarkerRE finds @opencode-agent:NAME in both HTML-comment and bare form.
var agentMarkerRE = regexp.MustCompile(`(?:<!--\s*)?@opencode-agent:([A-Za-z0-9_-]+)(?:\s*-->)?`)

// detectAgent scans the full request for an @opencode-agent:NAME marker.
// Checks the system field first, then every message's content. Falls back
// to "general" if no marker is found.
func detectAgent(req anthropicRequest) string {
	// System field: string or array of {type,text} blocks.
	if len(req.System) > 0 {
		if name := findInRaw(req.System); name != "" {
			return name
		}
	}
	// Messages: each content is string or array of blocks with text.
	for _, m := range req.Messages {
		if name := findInRaw(m.Content); name != "" {
			return name
		}
	}
	return defaultAgent
}

func findInRaw(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if m := agentMarkerRE.FindStringSubmatch(s); m != nil {
			return m[1]
		}
		return ""
	}
	var blocks []map[string]interface{}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			text, _ := b["text"].(string)
			if text == "" {
				continue
			}
			if m := agentMarkerRE.FindStringSubmatch(text); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// --- Prompt + tool_result extraction ---

// extractPrompt returns the most recent user text, concatenating text blocks
// and stripping <system-reminder> wrappers that Claude Code adds.
func extractPrompt(msgs []message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		raw := msgs[i].Content
		var s string
		if json.Unmarshal(raw, &s) == nil {
			s = stripSystemReminders(s)
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
			continue
		}
		var blocks []map[string]interface{}
		if json.Unmarshal(raw, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b["type"] != "text" {
					continue
				}
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			}
			joined := stripSystemReminders(strings.Join(parts, "\n"))
			if joined = strings.TrimSpace(joined); joined != "" {
				return joined
			}
		}
	}
	return ""
}

// extractLatestToolResult returns the content of the last tool_result block
// (opencode stdout from the bash tool Claude Code ran).
func extractLatestToolResult(msgs []message) (string, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		var blocks []map[string]interface{}
		if json.Unmarshal(msgs[i].Content, &blocks) != nil {
			continue
		}
		for j := len(blocks) - 1; j >= 0; j-- {
			b := blocks[j]
			if b["type"] != "tool_result" {
				continue
			}
			switch c := b["content"].(type) {
			case string:
				return c, true
			case []interface{}:
				var parts []string
				for _, cb := range c {
					cbm, ok := cb.(map[string]interface{})
					if !ok {
						continue
					}
					if t, ok := cbm["text"].(string); ok {
						parts = append(parts, t)
					}
				}
				return strings.Join(parts, "\n"), true
			}
		}
	}
	return "", false
}

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

// --- Opencode command + output parsing ---

// buildOpencodeCommand returns the shell command Claude Code will execute.
// OPENCODE_NO_DEBUG=1 suppresses the wrapper's INFO/DEBUG output. --format
// json makes stdout a newline-delimited event stream so we can extract the
// sessionID and assistant text deterministically.
func buildOpencodeCommand(agent, prompt, sessionID string) string {
	var b strings.Builder
	b.WriteString("OPENCODE_NO_DEBUG=1 opencode run --format json")
	if sessionID != "" {
		b.WriteString(" --session ")
		b.WriteString(shellQuote(sessionID)) // comes from command output: never trust it in a shell
	}
	if agent != "" {
		b.WriteString(" --agent ")
		b.WriteString(agent)
	}
	b.WriteString(" ")
	b.WriteString(shellQuote(prompt))
	return b.String()
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// parseOpencodeOutput walks newline-delimited JSON events from `opencode run
// --format json` and returns (sessionID, assistantText). Non-JSON lines are
// ignored. If no JSON at all, returns ("", raw) so we still show output.
func parseOpencodeOutput(out string) (sessionID, text string) {
	var parts []string
	sawJSON := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		sawJSON = true
		if ev.SessionID != "" && sessionID == "" {
			sessionID = ev.SessionID
		}
		if ev.Type == "text" && ev.Part.Text != "" {
			parts = append(parts, ev.Part.Text)
		}
	}
	if !sawJSON {
		return "", out
	}
	return sessionID, strings.Join(parts, "")
}

// --- Anthropic response writers ---

func writeJSONToolUse(w http.ResponseWriter, model, command string) {
	writeMessage(w, model, false, toolUseBlock(command), "tool_use")
}

func writeJSONText(w http.ResponseWriter, model, text string) {
	writeMessage(w, model, false, map[string]any{"type": "text", "text": text}, "end_turn")
}

func writeSSEToolUse(w http.ResponseWriter, model, command string) {
	writeMessage(w, model, true, toolUseBlock(command), "tool_use")
}

func writeSSEText(w http.ResponseWriter, model, text string) {
	writeMessage(w, model, true, map[string]any{"type": "text", "text": text}, "end_turn")
}

func toolUseBlock(command string) map[string]any {
	return map[string]any{
		"type": "tool_use", "id": generateID("toolu_"), "name": "Bash",
		"input": map[string]any{"command": command, "description": "opencode bridge"},
	}
}

// writeMessage sends one assistant message with a single content block,
// as JSON or as an SSE stream.
func writeMessage(w http.ResponseWriter, model string, stream bool, block map[string]any, stop string) {
	id := generateID("msg_")
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model,
			"content": []any{block}, "stop_reason": stop, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 1},
		})
		return
	}
	sse := anthropic.StartSSE(w)
	sse.Event("message_start", anthropic.MessageStart(id, model, 0))
	start, delta := map[string]any{"type": "text", "text": ""}, map[string]any{"type": "text_delta", "text": block["text"]}
	if block["type"] == "tool_use" {
		input, _ := json.Marshal(block["input"])
		start = map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
	}
	sse.Event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": start})
	sse.Event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
	sse.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sse.Event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": 1},
	})
	sse.Event("message_stop", map[string]any{"type": "message_stop"})
}

// --- helpers ---

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func generateID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
