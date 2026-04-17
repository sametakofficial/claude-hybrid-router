package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
)

// TestOpencodeSessionReuse_FullCONNECTStack stands up the whole MITM
// proxy and sends three CONNECT+TLS requests through it, using a stub
// `opencode` on PATH that emits deterministic JSON events. It proves
// the wiring end-to-end:
//
//   Turn 1: request with @proxy-local-route marker →
//           proxy returns tool_use(Bash) with --format json
//   Turn 2: tool_result carrying the stub's JSON output →
//           proxy extracts sessionID, caches it, returns assistant text
//   Turn 3: a follow-up prompt for the same agent →
//           proxy returns tool_use(Bash) with --session <id>
//
// Uses CONNECT+TLS (same path Claude Code takes), so regressions in
// request parsing, marker matching, resolver lookup, command expansion,
// JSON parsing, or session cache would all surface here.
func TestOpencodeSessionReuse_FullCONNECTStack(t *testing.T) {
	orig := defaultOpencodeSessions
	defaultOpencodeSessions = newOpencodeSessionCache()
	t.Cleanup(func() { defaultOpencodeSessions = orig })

	// --- Stub opencode on PATH that emits canned JSON events.
	// We bake the sessionID and response text into the stub so the test
	// is deterministic and doesn't hit a real provider.
	const stubSID = "ses_E2E_STUB_0001"
	const stubText = "banana 7"
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "opencode")
	stubScript := fmt.Sprintf(`#!/bin/bash
# stub opencode — prints deterministic JSON events so the test is
# hermetic. Real opencode output shape was captured from opencode
# run -m opencode/gpt-5-nano --format json on 2026-04-17.
cat <<EOF
{"type":"step_start","timestamp":1776446874588,"sessionID":"%s","part":{"type":"step-start"}}
{"type":"text","timestamp":1776446881564,"sessionID":"%s","part":{"type":"text","text":"%s"}}
{"type":"step_finish","timestamp":1776446881621,"sessionID":"%s","part":{"type":"step-finish","reason":"stop"}}
EOF
`, stubSID, stubSID, stubText, stubSID)
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	prevPath := os.Getenv("PATH")
	os.Setenv("PATH", stubDir+string(os.PathListSeparator)+prevPath)
	t.Cleanup(func() { os.Setenv("PATH", prevPath) })

	// --- Build a command-bridge provider pointing at opencode.
	const agent = "simplifier"
	const label = "oc_simplify"
	resolver, err := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:    "opencode-stub",
			Command: "opencode run --agent $AGENT --format default --dir /tmp '$PROMPT'",
			Models:  map[string]config.ModelConfig{label: {Model: agent}},
		}},
	})
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}
	infra := setupInfra(t, resolver)

	marker := "<!-- @proxy-local-route:af83e9 model=" + label + " -->"

	// --- Turn 1: fresh prompt.
	turn1 := mustMarshal(t, map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"system":     marker + " You are helpful",
		"messages":   []map[string]any{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
	})
	status, body, _ := proxyRequest(t, infra, "POST", "/v1/messages", turn1, nil)
	if status != 200 {
		t.Fatalf("turn1 status=%d body=%s", status, body)
	}
	t1Cmd := cmdFromToolUse(t, body)
	t.Logf("turn1 cmd: %s", t1Cmd)
	if !strings.Contains(t1Cmd, "--format json") {
		t.Errorf("turn1 missing --format json: %s", t1Cmd)
	}
	if strings.Contains(t1Cmd, "--session") {
		t.Errorf("turn1 should not inject --session on first call: %s", t1Cmd)
	}

	// Run the stub (what Claude Code's Bash tool would do).
	stubOutput := runBash(t, t1Cmd)
	if !strings.Contains(stubOutput, stubSID) {
		t.Fatalf("stub did not emit sessionID: %s", stubOutput)
	}

	// --- Turn 2: deliver stub output as tool_result.
	turn2 := mustMarshal(t, map[string]any{
		"model":  "claude-sonnet-4-20250514",
		"system": marker + " You are helpful",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": stubOutput},
			},
		}},
		"max_tokens": 1024,
	})
	status, body, _ = proxyRequest(t, infra, "POST", "/v1/messages", turn2, nil)
	if status != 200 {
		t.Fatalf("turn2 status=%d body=%s", status, body)
	}
	reply := assistantTextFromResp(t, body)
	if reply != stubText {
		t.Errorf("turn2 assistant text: got %q want %q", reply, stubText)
	}
	if cached := defaultOpencodeSessions.Get(agent); cached != stubSID {
		t.Fatalf("after turn2 cache should hold %q, got %q", stubSID, cached)
	}

	// --- Turn 3: follow-up prompt for the same agent — command must
	// carry --session <cached-id>.
	turn3 := mustMarshal(t, map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"system":     marker + " You are helpful",
		"messages":   []map[string]any{{"role": "user", "content": "what did you say?"}},
		"max_tokens": 1024,
	})
	status, body, _ = proxyRequest(t, infra, "POST", "/v1/messages", turn3, nil)
	if status != 200 {
		t.Fatalf("turn3 status=%d body=%s", status, body)
	}
	t3Cmd := cmdFromToolUse(t, body)
	t.Logf("turn3 cmd: %s", t3Cmd)
	expect := "--session " + stubSID
	if !strings.Contains(t3Cmd, expect) {
		t.Errorf("turn3 missing %q in command: %s", expect, t3Cmd)
	}
	if !strings.Contains(t3Cmd, "what did you say?") {
		t.Errorf("turn3 prompt not propagated: %s", t3Cmd)
	}
}

// --- helpers exclusive to this file (keep separate from unit-test helpers). ---

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func cmdFromToolUse(t *testing.T, respBody string) string {
	t.Helper()
	var r struct {
		Content []struct {
			Input struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(respBody), &r); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, respBody)
	}
	if len(r.Content) == 0 {
		t.Fatalf("no content blocks: %s", respBody)
	}
	return r.Content[0].Input.Command
}

func assistantTextFromResp(t *testing.T, respBody string) string {
	t.Helper()
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(respBody), &r); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, respBody)
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func runBash(t *testing.T, cmd string) string {
	t.Helper()
	// exec with bash -c so user-supplied quoting behaves like it would
	// under Claude Code's Bash tool.
	out, err := execCombined("bash", "-c", cmd)
	if err != nil {
		t.Fatalf("stub run %q: %v\noutput=%s", cmd, err, out)
	}
	return out
}
