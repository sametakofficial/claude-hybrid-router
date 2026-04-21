package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestDetectAgent_FromSystemString(t *testing.T) {
	req := anthropicRequest{
		System: mustMarshal(t, "hello <!-- @opencode-agent:simplifier --> world"),
	}
	if got := detectAgent(req); got != "simplifier" {
		t.Errorf("got %q, want simplifier", got)
	}
}

func TestDetectAgent_FromSystemBlocks(t *testing.T) {
	req := anthropicRequest{
		System: mustMarshal(t, []map[string]string{
			{"type": "text", "text": "prefix"},
			{"type": "text", "text": "<!-- @opencode-agent:reviewer --> here"},
		}),
	}
	if got := detectAgent(req); got != "reviewer" {
		t.Errorf("got %q, want reviewer", got)
	}
}

func TestDetectAgent_FromUserMessage(t *testing.T) {
	req := anthropicRequest{
		Messages: []message{{
			Role:    "user",
			Content: mustMarshal(t, "need help <!-- @opencode-agent:research-agent -->"),
		}},
	}
	if got := detectAgent(req); got != "research-agent" {
		t.Errorf("got %q, want research-agent", got)
	}
}

func TestDetectAgent_FallbackToGeneral(t *testing.T) {
	req := anthropicRequest{
		System:   mustMarshal(t, "You are helpful"),
		Messages: []message{{Role: "user", Content: mustMarshal(t, "hi")}},
	}
	if got := detectAgent(req); got != "general" {
		t.Errorf("got %q, want general fallback", got)
	}
}

func TestExtractPrompt_StripsSystemReminders(t *testing.T) {
	msgs := []message{
		{Role: "user", Content: mustMarshal(t,
			"<system-reminder>noise</system-reminder>\nactual question")},
	}
	if got := extractPrompt(msgs); got != "actual question" {
		t.Errorf("got %q", got)
	}
}

func TestExtractPrompt_WalksBackwards(t *testing.T) {
	msgs := []message{
		{Role: "user", Content: mustMarshal(t, "first")},
		{Role: "assistant", Content: mustMarshal(t, "answer")},
		{Role: "user", Content: mustMarshal(t, "follow-up")},
	}
	if got := extractPrompt(msgs); got != "follow-up" {
		t.Errorf("got %q", got)
	}
}

func TestExtractLatestToolResult_String(t *testing.T) {
	msgs := []message{
		{Role: "user", Content: mustMarshal(t, []map[string]interface{}{
			{"type": "tool_result", "content": "the stdout"},
		})},
	}
	got, ok := extractLatestToolResult(msgs)
	if !ok || got != "the stdout" {
		t.Errorf("got (%q, %v), want (\"the stdout\", true)", got, ok)
	}
}

func TestExtractLatestToolResult_BlockArray(t *testing.T) {
	msgs := []message{
		{Role: "user", Content: mustMarshal(t, []map[string]interface{}{
			{"type": "tool_result", "content": []map[string]string{
				{"type": "text", "text": "line 1"},
				{"type": "text", "text": "line 2"},
			}},
		})},
	}
	got, ok := extractLatestToolResult(msgs)
	if !ok || !strings.Contains(got, "line 1") || !strings.Contains(got, "line 2") {
		t.Errorf("got (%q, %v)", got, ok)
	}
}

func TestExtractLatestToolResult_NonePresent(t *testing.T) {
	msgs := []message{
		{Role: "user", Content: mustMarshal(t, "plain text")},
	}
	if _, ok := extractLatestToolResult(msgs); ok {
		t.Error("expected false for plain user message")
	}
}

func TestBuildOpencodeCommand(t *testing.T) {
	cases := []struct {
		name      string
		agent     string
		prompt    string
		sessionID string
		wantHas   []string
		wantOmits []string
	}{
		{
			name: "fresh session + agent",
			agent: "simplifier", prompt: "hello",
			wantHas:   []string{"OPENCODE_NO_DEBUG=1", "opencode run --format json", "--agent simplifier", "'hello'"},
			wantOmits: []string{"--session"},
		},
		{
			name: "resumed session",
			agent: "simplifier", prompt: "hello", sessionID: "ses_abc",
			wantHas: []string{"--session ses_abc", "--agent simplifier"},
		},
		{
			name: "quote escape",
			agent: "general", prompt: "what's up",
			wantHas: []string{`'what'"'"'s up'`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildOpencodeCommand(tc.agent, tc.prompt, tc.sessionID)
			for _, h := range tc.wantHas {
				if !strings.Contains(got, h) {
					t.Errorf("missing %q in %q", h, got)
				}
			}
			for _, o := range tc.wantOmits {
				if strings.Contains(got, o) {
					t.Errorf("unexpected %q in %q", o, got)
				}
			}
		})
	}
}

func TestParseOpencodeOutput_ExtractsSessionAndText(t *testing.T) {
	out := `{"type":"session","sessionID":"ses_123"}
{"type":"text","part":{"text":"Hello "},"sessionID":"ses_123"}
{"type":"text","part":{"text":"world"},"sessionID":"ses_123"}
{"type":"done","sessionID":"ses_123"}
`
	sid, text := parseOpencodeOutput(out)
	if sid != "ses_123" {
		t.Errorf("sid=%q, want ses_123", sid)
	}
	if text != "Hello world" {
		t.Errorf("text=%q, want 'Hello world'", text)
	}
}

func TestParseOpencodeOutput_NoJSON_RawPassthrough(t *testing.T) {
	sid, text := parseOpencodeOutput("plain text")
	if sid != "" || text != "plain text" {
		t.Errorf("got (%q, %q)", sid, text)
	}
}

func TestParseOpencodeOutput_SkipsNonJSONLines(t *testing.T) {
	out := `warning: foo
{"type":"text","part":{"text":"ok"},"sessionID":"ses_1"}
stray
`
	sid, text := parseOpencodeOutput(out)
	if sid != "ses_1" || text != "ok" {
		t.Errorf("sid=%q text=%q", sid, text)
	}
}

func TestStripSystemReminders(t *testing.T) {
	in := "<system-reminder>A</system-reminder>middle<system-reminder>B</system-reminder>end"
	if got := stripSystemReminders(in); got != "middleend" {
		t.Errorf("got %q", got)
	}
	in2 := "before<system-reminder>unclosed"
	if got := stripSystemReminders(in2); got != "before" {
		t.Errorf("unclosed: got %q", got)
	}
}

// --- session cache tests ---

func TestSessionCache_SetGet(t *testing.T) {
	c := newSessionCache("", time.Hour)
	c.Set("simplifier", "ses_aaa")
	if got := c.Get("simplifier"); got != "ses_aaa" {
		t.Errorf("got %q", got)
	}
	if got := c.Get("nonexistent"); got != "" {
		t.Errorf("got %q for missing", got)
	}
}

func TestSessionCache_TTLExpiry(t *testing.T) {
	c := newSessionCache("", 10*time.Millisecond)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Set("simplifier", "ses_xyz")
	now = now.Add(20 * time.Millisecond)
	if got := c.Get("simplifier"); got != "" {
		t.Errorf("expected expired, got %q", got)
	}
}

func TestSessionCache_DiskPersistence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"

	c1 := newSessionCache(path, time.Hour)
	c1.Set("simplifier", "ses_persisted")

	c2 := newSessionCache(path, time.Hour)
	if got := c2.Get("simplifier"); got != "ses_persisted" {
		t.Errorf("got %q after reload, want ses_persisted", got)
	}
}

func TestSessionCache_SkipsStaleOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"

	c1 := newSessionCache(path, 10*time.Millisecond)
	c1.Set("simplifier", "ses_old")

	time.Sleep(25 * time.Millisecond)

	c2 := newSessionCache(path, 10*time.Millisecond)
	if got := c2.Get("simplifier"); got != "" {
		t.Errorf("expected stale drop, got %q", got)
	}
}
