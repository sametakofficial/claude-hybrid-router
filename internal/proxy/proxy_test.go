package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
)

type captured struct {
	mu     sync.Mutex
	hits   int
	path   string
	header http.Header
	body   string
}

func (c *captured) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits++
	c.path, c.header, c.body = r.URL.Path, r.Header.Clone(), string(b)
}

type env struct {
	t        *testing.T
	proxy    *httptest.Server
	upstream *captured
	openai   *captured
	anth     *captured
	rec      *stats.Recorder
	cfg      *config.Config

	openaiStatus int
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, upstream: &captured{}, openai: &captured{}, anth: &captured{}, openaiStatus: 200}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.upstream.record(r)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"from anthropic"}],"usage":{"input_tokens":3,"cache_read_input_tokens":2,"output_tokens":4}}`)
	}))
	oa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.openai.record(r)
		if e.openaiStatus != 200 {
			w.WriteHeader(e.openaiStatus)
			io.WriteString(w, `{"error":{"message":"bad key sk-abcdefghijklmnop"}}`)
			return
		}
		var req map[string]any
		json.Unmarshal([]byte(e.openai.body), &req)
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi from gpt\"}}]}\n\n")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"message":{"content":"hi from gpt"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":2}}`)
	}))
	an := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.anth.record(r)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"from glm"}],"usage":{"input_tokens":5,"output_tokens":6}}`)
	}))
	t.Cleanup(func() { up.Close(); oa.Close(); an.Close() })

	e.cfg = config.Default()
	e.cfg.Server.Upstream = up.URL
	e.cfg.Providers = []config.Provider{
		{ID: "gpt", Type: config.TypeOpenAI, BaseURL: oa.URL + "/v1", APIKey: "sk-provider", Enabled: true},
		{ID: "glm", Type: config.TypeAnthropic, BaseURL: an.URL, APIKey: "glm-key", Enabled: true},
		{ID: "off", Type: config.TypeOpenAI, BaseURL: oa.URL, Enabled: false},
	}
	e.cfg.Agents = map[string]config.Route{
		"coder":  {Provider: "gpt", Model: "gpt-5", SystemMode: config.SystemAppend, SystemPrompt: "EXTRA"},
		"broken": {Provider: "off"},
	}
	e.cfg.Rules = []config.Rule{{Match: "claude-haiku-*", Enabled: true, Route: config.Route{Provider: "glm", Model: "glm-4.6"}}}
	if err := e.cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e.rec = stats.New(100, nil)
	e.proxy = httptest.NewServer(New(func() *config.Config { return e.cfg }, e.rec))
	t.Cleanup(e.proxy.Close)
	return e
}

func (e *env) post(path, body string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.proxy.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer oauth-subscription-token")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// entries waits for the log entries: they are written right after the
// response body is finished, so a fast client can get there first.
func (e *env) entries(n int) []stats.Entry {
	for i := 0; i < 100; i++ {
		if got := e.rec.Since(0); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return e.rec.Since(0)
}

const marker = "<!-- claude-hybrid:agent=coder -->"

func TestPlainRequestGoesToAnthropicUnchanged(t *testing.T) {
	e := newEnv(t)
	body := `{"model":"claude-opus","max_tokens":10,"system":"You are Claude Code","messages":[{"role":"user","content":"hi"}]}`
	resp, out := e.post("/v1/messages?beta=true", body)
	if resp.StatusCode != 200 || !strings.Contains(out, "from anthropic") {
		t.Fatalf("got %d %s", resp.StatusCode, out)
	}
	if e.upstream.body != body {
		t.Fatalf("body must be byte-identical:\n%s", e.upstream.body)
	}
	if e.upstream.header.Get("Authorization") != "Bearer oauth-subscription-token" {
		t.Fatal("subscription auth must reach Anthropic")
	}
	if e.openai.hits+e.anth.hits != 0 {
		t.Fatal("providers must not be hit")
	}
	e.entries(1)
	if s := e.rec.Summary(); s.Requests != 1 || s.Targets[0].InputTokens != 5 || s.Targets[0].OutputTokens != 4 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestAgentMarkerRoutesToOpenAIStreaming(t *testing.T) {
	e := newEnv(t)
	body := `{"model":"claude-sonnet","max_tokens":10,"stream":true,"system":[{"type":"text","text":"base"},{"type":"text","text":"` + strings.ReplaceAll(marker, `"`, `\"`) + `\nYou are coder"}],"messages":[{"role":"user","content":"hi"}]}`
	resp, out := e.post("/v1/messages", body)
	if resp.StatusCode != 200 || !strings.Contains(out, "event: message_start") || !strings.Contains(out, "hi from gpt") {
		t.Fatalf("got %d %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `"model":"claude-sonnet"`) {
		t.Fatal("reply should keep the requested model name")
	}
	if e.upstream.hits != 0 {
		t.Fatal("anthropic must not be hit")
	}
	if got := e.openai.header.Get("Authorization"); got != "Bearer sk-provider" {
		t.Fatalf("provider auth = %q", got)
	}
	if strings.Contains(e.openai.body, "claude-hybrid:agent") || strings.Contains(e.openai.body, "oauth") {
		t.Fatalf("marker/token leaked: %s", e.openai.body)
	}
	if !strings.Contains(e.openai.body, `"model":"gpt-5"`) || !strings.Contains(e.openai.body, "EXTRA") || !strings.Contains(e.openai.body, "You are coder") {
		t.Fatalf("route options not applied: %s", e.openai.body)
	}
	got := e.entries(1)
	if len(got) != 1 || got[0].Source != "agent:coder" || got[0].InputTokens != 11 || got[0].Status != 200 {
		t.Fatalf("log entry: %+v", got)
	}
}

func TestAgentMarkerNonStreaming(t *testing.T) {
	e := newEnv(t)
	body := `{"model":"claude-sonnet","max_tokens":10,"system":"` + strings.ReplaceAll(marker, `"`, `\"`) + `","messages":[{"role":"user","content":"hi"}]}`
	resp, out := e.post("/v1/messages", body)
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	if resp.StatusCode != 200 || m["type"] != "message" || !strings.Contains(out, "hi from gpt") {
		t.Fatalf("got %d %s", resp.StatusCode, out)
	}
}

func TestUnroutedAgentGoesToAnthropicWithoutMarker(t *testing.T) {
	e := newEnv(t)
	body := `{"model":"claude-sonnet","system":"<!-- claude-hybrid:agent=plain -->\nYou are plain","messages":[{"role":"user","content":"hi"}]}`
	e.post("/v1/messages", body)
	if e.upstream.hits != 1 || strings.Contains(e.upstream.body, "claude-hybrid:agent") || !strings.Contains(e.upstream.body, "You are plain") {
		t.Fatalf("upstream body: %s", e.upstream.body)
	}
	if e.entries(1)[0].Source != "agent:plain" {
		t.Fatal("source should name the agent")
	}
}

func TestMarkerInUserMessageIsIgnored(t *testing.T) {
	e := newEnv(t)
	body := `{"model":"claude-opus","system":"main","messages":[{"role":"user","content":[{"type":"text","text":"` + strings.ReplaceAll(marker, `"`, `\"`) + `"}]}]}`
	e.post("/v1/messages", body)
	if e.upstream.hits != 1 || e.openai.hits != 0 {
		t.Fatal("marker outside system must not reroute")
	}
}

func TestRuleRoutesToAnthropicProvider(t *testing.T) {
	e := newEnv(t)
	resp, out := e.post("/v1/messages", `{"model":"claude-haiku-4","max_tokens":50,"messages":[{"role":"user","content":"title"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(out, "from glm") {
		t.Fatalf("got %d %s", resp.StatusCode, out)
	}
	h := e.anth.header
	if h.Get("x-api-key") != "glm-key" || strings.Contains(h.Get("Authorization"), "oauth") || h.Get("anthropic-beta") != "" {
		t.Fatalf("provider headers: %v", h)
	}
	if e.anth.path != "/v1/messages" || !strings.Contains(e.anth.body, `"model":"glm-4.6"`) {
		t.Fatalf("path %s body %s", e.anth.path, e.anth.body)
	}
	if got := e.entries(1)[0]; got.Source != "rule:1" || got.OutputTokens != 6 {
		t.Fatalf("entry %+v", got)
	}
}

func TestProviderAuthErrorIsNotPassedAs401(t *testing.T) {
	e := newEnv(t)
	e.openaiStatus = 401
	resp, out := e.post("/v1/messages", `{"model":"m","system":"`+strings.ReplaceAll(marker, `"`, `\"`)+`","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(out, `"type":"error"`) || strings.Contains(out, "abcdefghijklmnop") {
		t.Fatalf("error body should be Anthropic-shaped and redacted: %s", out)
	}
}

func TestDisabledProvider(t *testing.T) {
	e := newEnv(t)
	resp, out := e.post("/v1/messages", `{"model":"m","system":"<!-- claude-hybrid:agent=broken -->","messages":[]}`)
	if resp.StatusCode != 400 || !strings.Contains(out, "disabled") {
		t.Fatalf("got %d %s", resp.StatusCode, out)
	}
}

func TestCountTokensForOpenAIRouteIsEstimated(t *testing.T) {
	e := newEnv(t)
	resp, out := e.post("/v1/messages/count_tokens", `{"model":"m","system":"`+strings.ReplaceAll(marker, `"`, `\"`)+`","messages":[{"role":"user","content":"hello there"}]}`)
	if resp.StatusCode != 200 || !strings.Contains(out, "input_tokens") || e.openai.hits != 0 {
		t.Fatalf("got %d %s", resp.StatusCode, out)
	}
	if len(e.rec.Since(0)) != 0 {
		t.Fatal("count_tokens should not be logged")
	}
}

func TestOtherRequestsPassThrough(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.proxy.URL + "/api/hello")
	if err != nil || resp.StatusCode != 200 || e.upstream.path != "/api/hello" {
		t.Fatalf("GET passthrough failed: %v %v", err, e.upstream.path)
	}
	resp, _ = http.Get(e.proxy.URL + HealthPath)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "claude-hybrid") {
		t.Fatal("health")
	}
}

func TestLegacyMarker(t *testing.T) {
	e := newEnv(t)
	base := strings.TrimSuffix(e.cfg.Providers[1].BaseURL, "/")
	e.post("/v1/messages", `{"model":"m","system":"<!-- @proxy-local-route:af83e9 url=`+base+` -->","messages":[]}`)
	if e.anth.hits != 1 || strings.Contains(e.anth.body, "proxy-local-route") {
		t.Fatalf("legacy marker: hits=%d body=%s", e.anth.hits, e.anth.body)
	}
}

func TestBrowserAndForeignRequestsAreRejected(t *testing.T) {
	e := newEnv(t)
	body := `{"model":"claude-haiku-4","messages":[{"role":"user","content":"x"}]}`
	for name, set := range map[string]func(*http.Request){
		"origin":     func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"fetch-site": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"rebinding":  func(r *http.Request) { r.Host = "evil.example:3457" },
	} {
		req, _ := http.NewRequest(http.MethodPost, e.proxy.URL+"/v1/messages", strings.NewReader(body))
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, resp.StatusCode)
		}
	}
	if e.anth.hits+e.upstream.hits != 0 {
		t.Fatal("rejected requests must not reach any backend")
	}
}

func TestLegacyMarkerOnlyForLocalURLs(t *testing.T) {
	e := newEnv(t)
	e.post("/v1/messages", `{"model":"m","system":"<!-- @proxy-local-route:af83e9 url=https://attacker.example -->","messages":[]}`)
	if e.upstream.hits != 1 || e.anth.hits != 0 {
		t.Fatalf("remote legacy URL must be ignored (upstream=%d)", e.upstream.hits)
	}
}
