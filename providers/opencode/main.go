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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultAgent = "general"
	sessionTTL   = 2 * time.Hour
)

var (
	sessions     *sessionCache
	sessionsPath string
)

func main() {
	port := flag.Int("port", 4568, "port to listen on (Anthropic-compatible)")
	cachePath := flag.String("cache", defaultCachePath(),
		"JSON file for disk-persisted opencode session cache")
	flag.Parse()

	sessionsPath = *cachePath
	sessions = newSessionCache(sessionsPath, sessionTTL)

	http.HandleFunc("/v1/messages", handleMessages)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("opencode-bridge listening on %s (cache=%s)", addr, sessionsPath)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func defaultCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude-hybrid", "opencode-sessions.json")
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

func handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := readBody(r)
	if err != nil {
		writeError(w, 400, "invalid_request_error", "Failed to read request body")
		return
	}

	var req anthropicRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, 400, "invalid_request_error", "Failed to parse request body")
		return
	}

	agent := detectAgent(req)
	modelLabel := "opencode/" + agent

	// Turn 2 check: does the body contain a tool_result we should parse?
	if toolOut, ok := extractLatestToolResult(req.Messages); ok {
		sessionID, text := parseOpencodeOutput(toolOut)
		if sessionID != "" {
			sessions.Set(agent, sessionID)
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
		writeError(w, 400, "invalid_request_error", "No user message found")
		return
	}

	resumed := sessions.Get(agent)
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

// agentMarkerRE finds `<!-- @opencode-agent:NAME -->` anywhere in text.
// Also accepts the bare form `@opencode-agent:NAME` to survive markdown
// parsers that strip HTML comments.
var agentMarkerRE = regexp.MustCompile(`@opencode-agent:([A-Za-z0-9_-]+)`)

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
		b.WriteString(sessionID)
	}
	if agent != "" {
		b.WriteString(" --agent ")
		b.WriteString(agent)
	}
	b.WriteString(" '")
	b.WriteString(strings.ReplaceAll(prompt, "'", "'\"'\"'"))
	b.WriteString("'")
	return b.String()
}

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
	toolID := generateID("toolu_")
	resp := map[string]interface{}{
		"id":   generateID("msg_"),
		"type": "message",
		"role": "assistant",
		"content": []map[string]interface{}{
			{
				"type": "tool_use",
				"id":   toolID,
				"name": "Bash",
				"input": map[string]interface{}{
					"command":     command,
					"description": "opencode bridge",
				},
			},
		},
		"model":         model,
		"stop_reason":   "tool_use",
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": 0, "output_tokens": 1},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeJSONText(w http.ResponseWriter, model, text string) {
	resp := map[string]interface{}{
		"id":   generateID("msg_"),
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeSSEToolUse(w http.ResponseWriter, model, command string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONToolUse(w, model, command)
		return
	}
	toolID := generateID("toolu_")
	msgID := generateID("msg_")
	inputJSON, _ := json.Marshal(map[string]interface{}{
		"command":     command,
		"description": "opencode bridge",
	})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

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
		{"message_stop", map[string]interface{}{"type": "message_stop"}},
	}
	for _, ev := range events {
		b, _ := json.Marshal(ev.data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.event, b)
	}
	flusher.Flush()
}

func writeSSEText(w http.ResponseWriter, model, text string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONText(w, model, text)
		return
	}
	msgID := generateID("msg_")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)

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
		{"message_stop", map[string]interface{}{"type": "message_stop"}},
	}
	for _, ev := range events {
		b, _ := json.Marshal(ev.data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.event, b)
	}
	flusher.Flush()
}

func writeError(w http.ResponseWriter, status int, errType, msg string) {
	resp := map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": msg,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// --- helpers ---

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	var buf [1 << 20]byte // 1 MiB is more than enough for tool-use round-trips
	var total []byte
	for {
		n, err := r.Body.Read(buf[:])
		if n > 0 {
			total = append(total, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, err
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

const idChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

var idMu sync.Mutex
var idCounter uint64

func generateID(prefix string) string {
	idMu.Lock()
	defer idMu.Unlock()
	idCounter++
	now := time.Now().UnixNano()
	mix := uint64(now) ^ idCounter*2654435761
	b := make([]byte, 20)
	for i := range b {
		b[i] = idChars[mix%uint64(len(idChars))]
		mix = mix/uint64(len(idChars)) + 1
	}
	return prefix + string(b)
}
