package proxy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// --- helpers for the end-to-end test ---

// mustMarshalToolResultBody returns an Anthropic-style request body
// whose last user message is a tool_result carrying the given output
// string. This is what Claude Code sends in Turn 2.
func mustMarshalToolResultBody(t *testing.T, output string) []byte {
	t.Helper()
	body := map[string]any{
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": output},
				},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// extractCommandFromBashToolUse parses the JSON (non-streaming)
// response emitted by forwardCommand in Turn 1 and returns the Bash
// command it wrapped.
func extractCommandFromBashToolUse(t *testing.T, httpResp string) string {
	t.Helper()
	parts := strings.SplitN(httpResp, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed HTTP response: %q", httpResp)
	}
	var msg struct {
		Content []struct {
			Input struct {
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(parts[1]), &msg); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if len(msg.Content) == 0 {
		t.Fatalf("no content blocks")
	}
	return msg.Content[0].Input.Command
}

// extractAssistantTextFromJSONResponse parses the JSON response from a
// Turn 2 reply and returns the concatenated assistant text.
func extractAssistantTextFromJSONResponse(t *testing.T, httpResp string) string {
	t.Helper()
	parts := strings.SplitN(httpResp, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed HTTP response: %q", httpResp)
	}
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(parts[1]), &msg); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	var sb strings.Builder
	for _, c := range msg.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func TestOpencodeSessionCache_GetSetExpiry(t *testing.T) {
	fake := time.Unix(1_700_000_000, 0)
	c := &opencodeSessionCache{
		entries: map[string]opencodeSessionEntry{},
		ttl:     time.Hour,
		now:     func() time.Time { return fake },
	}

	if got := c.Get("simplifier"); got != "" {
		t.Fatalf("empty cache: want \"\", got %q", got)
	}

	c.Set("simplifier", "ses_abc")
	if got := c.Get("simplifier"); got != "ses_abc" {
		t.Fatalf("after set: want ses_abc, got %q", got)
	}

	// Different agent gets its own slot.
	c.Set("reality-check", "ses_xyz")
	if got := c.Get("simplifier"); got != "ses_abc" {
		t.Fatalf("cross-agent contamination: got %q", got)
	}
	if got := c.Get("reality-check"); got != "ses_xyz" {
		t.Fatalf("second agent: want ses_xyz, got %q", got)
	}

	// TTL: a Get within window refreshes lastAt so a further tick under
	// ttl still returns the value.
	fake = fake.Add(45 * time.Minute)
	if got := c.Get("simplifier"); got != "ses_abc" {
		t.Fatalf("within ttl after refresh: got %q", got)
	}
	fake = fake.Add(45 * time.Minute) // 90m total since Set but only 45m since last Get
	if got := c.Get("simplifier"); got != "ses_abc" {
		t.Fatalf("refreshed entry expired prematurely: got %q", got)
	}

	// Skip past TTL: no access for 2h should drop the entry.
	fake = fake.Add(2 * time.Hour)
	if got := c.Get("simplifier"); got != "" {
		t.Fatalf("after ttl: want \"\", got %q", got)
	}

	c.Set("simplifier", "ses_abc")
	c.Forget("simplifier")
	if got := c.Get("simplifier"); got != "" {
		t.Fatalf("after forget: got %q", got)
	}
}

func TestOpencodeSessionCache_EmptyInputs(t *testing.T) {
	c := newOpencodeSessionCache()
	c.Set("", "ses_x")              // empty agent, should no-op
	c.Set("agent", "")              // empty session, should no-op
	if got := c.Get("agent"); got != "" {
		t.Fatalf("empty session was stored: %q", got)
	}
	if got := c.Get(""); got != "" {
		t.Fatalf("empty agent lookup returned: %q", got)
	}
}

func TestIsOpencodeRun(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"opencode run --agent foo 'hi'", true},
		{"  opencode  run  --agent foo", true},
		{"/usr/bin/opencode run", true},
		{"opencode tui", false},
		{"echo opencode run", true}, // conservative: any token matches
		{"", false},
		{"openrouter run", false},
		{"opencode-run", false}, // hyphenated doesn't match
	}
	for _, tc := range tests {
		if got := isOpencodeRun(tc.in); got != tc.want {
			t.Errorf("isOpencodeRun(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestForceJSONFormat(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			name: "replace default with json",
			in:   "opencode run --agent simplifier --format default --dir /x 'hi'",
			want: "opencode run --agent simplifier --format json --dir /x 'hi'",
		},
		{
			name: "replace json idempotent",
			in:   "opencode run --agent simplifier --format json --dir /x 'hi'",
			want: "opencode run --agent simplifier --format json --dir /x 'hi'",
		},
		{
			name: "equals form",
			in:   "opencode run --agent simplifier --format=default 'hi'",
			want: "opencode run --agent simplifier --format=json 'hi'",
		},
		{
			name: "no format flag — inject",
			in:   "opencode run --agent simplifier --dir /x 'hi'",
			want: "opencode run --format json --agent simplifier --dir /x 'hi'",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := forceJSONFormat(tc.in); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestInjectSessionFlag(t *testing.T) {
	in := "opencode run --agent simplifier --format json --dir /x 'hi'"
	got := injectSessionFlag(in, "ses_abc")
	if !strings.Contains(got, "--session ses_abc") {
		t.Fatalf("missing --session: %q", got)
	}
	if !strings.HasPrefix(got, "opencode run --session ses_abc ") {
		t.Fatalf("--session not placed right after `opencode run`: %q", got)
	}
}

func TestRewriteOpencodeCommand(t *testing.T) {
	tmpl := "opencode run --agent $AGENT --format default --dir /x 'hi'"

	// No cached session: json forced, no --session present.
	got := rewriteOpencodeCommand(tmpl, "")
	if !strings.Contains(got, "--format json") {
		t.Errorf("expected --format json in %q", got)
	}
	if strings.Contains(got, "--session") {
		t.Errorf("unexpected --session with empty id: %q", got)
	}

	// Cached session: json + --session.
	got = rewriteOpencodeCommand(tmpl, "ses_abc")
	if !strings.Contains(got, "--format json") {
		t.Errorf("expected --format json: %q", got)
	}
	if !strings.Contains(got, "--session ses_abc") {
		t.Errorf("expected --session ses_abc: %q", got)
	}
	// `opencode run` first, then --session, then everything else.
	sessionIdx := strings.Index(got, "--session ses_abc")
	agentIdx := strings.Index(got, "--agent")
	if sessionIdx < 0 || agentIdx < 0 || sessionIdx > agentIdx {
		t.Errorf("--session should precede --agent: %q", got)
	}
}

func TestParseOpencodeOutput_RealJSON(t *testing.T) {
	// Abbreviated replay of the real opencode run --format json stream we
	// captured with opencode/gpt-5-nano.
	out := `{"type":"step_start","timestamp":1776446874588,"sessionID":"ses_26383f28fffe9EmYWW7Vjlue1u","part":{"type":"step-start"}}
{"type":"text","timestamp":1776446881564,"sessionID":"ses_26383f28fffe9EmYWW7Vjlue1u","part":{"type":"text","text":"Hello there, friend"}}
{"type":"step_finish","timestamp":1776446881621,"sessionID":"ses_26383f28fffe9EmYWW7Vjlue1u","part":{"type":"step-finish","reason":"stop"}}`

	sid, text, errored := parseOpencodeOutput(out)
	if errored {
		t.Errorf("unexpected errored=true on clean stream")
	}
	if sid != "ses_26383f28fffe9EmYWW7Vjlue1u" {
		t.Errorf("sessionID: got %q", sid)
	}
	if text != "Hello there, friend" {
		t.Errorf("text: got %q", text)
	}
}

func TestParseOpencodeOutput_MultipleTextParts(t *testing.T) {
	out := `{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"A "}}
{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"B"}}`
	sid, text, _ := parseOpencodeOutput(out)
	if sid != "ses_1" || text != "A B" {
		t.Errorf("got sid=%q text=%q", sid, text)
	}
}

func TestParseOpencodeOutput_ErrorEventStillCarriesSessionID(t *testing.T) {
	out := `{"type":"error","sessionID":"ses_err","error":{"name":"APIError"}}`
	sid, _, errored := parseOpencodeOutput(out)
	if sid != "ses_err" {
		t.Errorf("expected error event to still yield sessionID, got %q", sid)
	}
	if !errored {
		t.Errorf("expected errored=true on error event")
	}
}

func TestParseOpencodeOutput_NonJSONFallback(t *testing.T) {
	// Legacy non-json output should come back verbatim so we don't
	// accidentally regress the existing --format default path.
	out := "[93m! [0m agent simplifier not found\nsome other warning"
	sid, text, _ := parseOpencodeOutput(out)
	if sid != "" {
		t.Errorf("no JSON — sessionID should be empty, got %q", sid)
	}
	if text != out {
		t.Errorf("non-json fallback: got %q\nwant %q", text, out)
	}
}

// End-to-end: Turn 1 → Turn 2 (sessionID captured) → Turn 3 (sessionID
// reused). Exercises the real forwardCommand wiring, proving the cache
// and the command rewrite talk to each other correctly across calls.
func TestOpencodeSessionReuseAcrossTurns(t *testing.T) {
	// Isolate from other tests — swap in a fresh cache and restore on exit.
	orig := defaultOpencodeSessions
	defaultOpencodeSessions = newOpencodeSessionCache()
	t.Cleanup(func() { defaultOpencodeSessions = orig })

	agent := "simplifier"
	tmpl := "opencode run --agent $AGENT --format default --dir /x '$PROMPT'"

	// --- Turn 1: fresh prompt, no cached session yet.
	turn1Body := `{"messages":[{"role":"user","content":"hello"}]}`
	var buf strings.Builder
	p := &Proxy{}
	p.forwardCommand(&buf, tmpl, agent, "oc_simplify", []byte(turn1Body), false)

	turn1Cmd := extractCommandFromBashToolUse(t, buf.String())
	if !strings.Contains(turn1Cmd, "--format json") {
		t.Errorf("turn1 missing --format json: %q", turn1Cmd)
	}
	if strings.Contains(turn1Cmd, "--session") {
		t.Errorf("turn1 should not have --session (cache empty): %q", turn1Cmd)
	}

	// --- Turn 2: simulate Claude returning the opencode JSON output as
	// tool_result. The proxy must parse it and cache the sessionID.
	turn2Output := `{"type":"step_start","sessionID":"ses_fromT2","part":{"type":"step-start"}}
{"type":"text","sessionID":"ses_fromT2","part":{"type":"text","text":"answer one"}}`
	turn2Body := mustMarshalToolResultBody(t, turn2Output)
	buf.Reset()
	p.forwardCommand(&buf, tmpl, agent, "oc_simplify", turn2Body, false)

	turn2Reply := extractAssistantTextFromJSONResponse(t, buf.String())
	if turn2Reply != "answer one" {
		t.Errorf("turn2 assistant text: got %q want %q", turn2Reply, "answer one")
	}
	if got := defaultOpencodeSessions.Get(agent); got != "ses_fromT2" {
		t.Fatalf("cache miss after turn2: got %q", got)
	}

	// --- Turn 3: another fresh prompt for the same agent. Now the
	// rewritten command must carry --session ses_fromT2.
	turn3Body := `{"messages":[{"role":"user","content":"follow-up"}]}`
	buf.Reset()
	p.forwardCommand(&buf, tmpl, agent, "oc_simplify", []byte(turn3Body), false)

	turn3Cmd := extractCommandFromBashToolUse(t, buf.String())
	if !strings.Contains(turn3Cmd, "--session ses_fromT2") {
		t.Errorf("turn3 missing resumed session: %q", turn3Cmd)
	}
	if !strings.Contains(turn3Cmd, "follow-up") {
		t.Errorf("turn3 missing prompt: %q", turn3Cmd)
	}

	// Different agent must not share the cached session.
	buf.Reset()
	otherBody := `{"messages":[{"role":"user","content":"different agent"}]}`
	p.forwardCommand(&buf, tmpl, "reality-check", "oc_reality-check", []byte(otherBody), false)
	otherCmd := extractCommandFromBashToolUse(t, buf.String())
	if strings.Contains(otherCmd, "--session") {
		t.Errorf("cross-agent leak — other agent got --session: %q", otherCmd)
	}
}

func TestIsOpencodeFailure(t *testing.T) {
	cases := []struct {
		name    string
		sid     string
		text    string
		raw     string
		errored bool
		want    bool
	}{
		{"clean success", "ses_1", "answer", "...json...", false, false},
		{"error event", "ses_1", "partial", "", true, true},
		{"bash timeout marker", "", "", "Command timed out after 10m 0.0s", false, true},
		{"no session and no text", "", "", "", false, true},
		{"no session but text present", "", "fallback plain text", "fallback plain text", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isOpencodeFailure(tc.sid, tc.text, tc.raw, tc.errored)
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestForwardCommandTurn2_ForgetsSessionOnTimeout(t *testing.T) {
	orig := defaultOpencodeSessions
	defaultOpencodeSessions = newOpencodeSessionCache()
	t.Cleanup(func() { defaultOpencodeSessions = orig })

	defaultOpencodeSessions.Set("simplifier", "ses_old")

	tmpl := "opencode run --agent $AGENT --format default --dir /x '$PROMPT'"
	body := mustMarshalToolResultBody(t, "Command timed out after 10m 0.0s\n\n<stderr>opencode hung</stderr>")

	var buf strings.Builder
	(&Proxy{}).forwardCommand(&buf, tmpl, "simplifier", "oc_simplify", body, false)

	if got := defaultOpencodeSessions.Get("simplifier"); got != "" {
		t.Fatalf("expected session forgotten on timeout, still have %q", got)
	}
}

func TestForwardCommandTurn2_ForgetsSessionOnEmptyOutput(t *testing.T) {
	orig := defaultOpencodeSessions
	defaultOpencodeSessions = newOpencodeSessionCache()
	t.Cleanup(func() { defaultOpencodeSessions = orig })

	defaultOpencodeSessions.Set("simplifier", "ses_old")

	tmpl := "opencode run --agent $AGENT --format default --dir /x '$PROMPT'"
	body := mustMarshalToolResultBody(t, "")

	var buf strings.Builder
	(&Proxy{}).forwardCommand(&buf, tmpl, "simplifier", "oc_simplify", body, false)

	if got := defaultOpencodeSessions.Get("simplifier"); got != "" {
		t.Fatalf("expected session forgotten on empty output, still have %q", got)
	}
}

func TestForwardCommandTurn1_IncludesBashTimeout(t *testing.T) {
	tmpl := "opencode run --agent $AGENT --format default --dir /x '$PROMPT'"
	body := `{"messages":[{"role":"user","content":"hello"}]}`

	var buf strings.Builder
	(&Proxy{}).forwardCommand(&buf, tmpl, "simplifier", "oc_simplify", []byte(body), false)

	parts := strings.SplitN(buf.String(), "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed response")
	}
	var msg struct {
		Content []struct {
			Input struct {
				Timeout int `json:"timeout"`
			} `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(parts[1]), &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(msg.Content) == 0 || msg.Content[0].Input.Timeout <= 0 {
		t.Fatalf("expected positive timeout in Bash input, got %+v", msg.Content)
	}
}

func TestForwardCommandTurn1_SSE_IncludesBashTimeout(t *testing.T) {
	tmpl := "opencode run --agent $AGENT --format default --dir /x '$PROMPT'"
	body := `{"messages":[{"role":"user","content":"hello"}]}`

	var buf strings.Builder
	(&Proxy{}).forwardCommand(&buf, tmpl, "simplifier", "oc_simplify", []byte(body), true)

	if !strings.Contains(buf.String(), `\"timeout\":`) {
		t.Fatalf("SSE tool_use missing timeout field: %q", buf.String())
	}
}

func TestParseOpencodeOutput_StrayStderrLinesIgnored(t *testing.T) {
	// opencode sometimes emits an ANSI warning line before JSON. Parser
	// must keep going and still extract sessionID/text.
	out := "\x1b[93m! \x1b[0m warning: blah\n" +
		`{"type":"text","sessionID":"ses_ok","part":{"type":"text","text":"hi"}}`
	sid, text, _ := parseOpencodeOutput(out)
	if sid != "ses_ok" || text != "hi" {
		t.Errorf("got sid=%q text=%q", sid, text)
	}
}
