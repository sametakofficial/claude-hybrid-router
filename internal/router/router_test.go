package router

import (
	"encoding/json"
	"testing"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func testConfig() *config.Config {
	c := config.Default()
	c.Providers = []config.Provider{{ID: "gpt", Type: config.TypeOpenAI, BaseURL: "https://x/v1", Enabled: true}}
	c.Agents = map[string]config.Route{"coder": {Provider: "gpt", Model: "gpt-5"}}
	c.Rules = []config.Rule{
		{Match: "claude-haiku-*", Enabled: false, Route: config.Route{Provider: "gpt", Model: "off"}},
		{Match: "claude-haiku-*", Enabled: true, Route: config.Route{Provider: "gpt", Model: "mini"}},
	}
	return c
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name, body string
		source     string
		sub        bool
		model      string
		bodyKept   bool
		system     any // expected system after marker removal; nil = absent
	}{
		{"plain", `{"model":"claude-opus","system":"main"}`, "", true, "", true, "main"},
		{"agent string", `{"model":"claude-opus","system":"<!-- claude-hybrid:agent=coder -->\nYou code"}`, "agent:coder", false, "gpt-5", false, "You code"},
		{"agent wins over rule", `{"model":"claude-haiku-4","system":[{"type":"text","text":"x"},{"type":"text","text":"<!-- claude-hybrid:agent=coder -->"}]}`, "agent:coder", false, "gpt-5", false, []any{map[string]any{"type": "text", "text": "x"}}},
		{"unrouted agent", `{"model":"claude-haiku-4","system":"<!-- claude-hybrid:agent=other -->"}`, "agent:other", true, "", false, nil},
		{"first enabled rule", `{"model":"claude-haiku-4"}`, "rule:2", false, "mini", true, nil},
		{"marker in message ignored", `{"model":"claude-opus","messages":[{"role":"user","content":"<!-- claude-hybrid:agent=coder -->"}]}`, "", true, "", true, nil},
		{"legacy local", `{"model":"m","system":"<!-- @proxy-local-route:af83e9 url=http://127.0.0.1:4568 -->"}`, "legacy", false, "", false, nil},
		{"legacy remote ignored", `{"model":"m","system":"<!-- @proxy-local-route:af83e9 url=https://evil.example -->"}`, "", true, "", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Decide(testConfig(), []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if d.Source() != tc.source || d.Subscription() != tc.sub || d.Route.Model != tc.model {
				t.Fatalf("got source=%q sub=%v model=%q", d.Source(), d.Subscription(), d.Route.Model)
			}
			if (string(d.Body) == tc.body) != tc.bodyKept {
				t.Fatalf("body kept = %v, want %v: %s", string(d.Body) == tc.body, tc.bodyKept, d.Body)
			}
			var sent map[string]any
			json.Unmarshal(d.Body, &sent)
			got, _ := json.Marshal(sent["system"])
			want, _ := json.Marshal(tc.system)
			if string(got) != string(want) {
				t.Fatalf("system = %s, want %s", got, want)
			}
		})
	}
	if d, err := Decide(testConfig(), []byte("not json")); err == nil || !d.Subscription() || string(d.Body) != "not json" {
		t.Fatal("bad JSON passes through")
	}
}

func TestApply(t *testing.T) {
	sys := func(body string, rt config.Route) string {
		r, _ := anthropic.ParseRequest([]byte(body))
		Apply(r, rt)
		b, _ := json.Marshal(r["system"])
		return string(b)
	}
	if got := sys(`{"system":"base"}`, config.Route{SystemMode: config.SystemAppend, SystemPrompt: "more"}); got != `[{"text":"base","type":"text"},{"text":"more","type":"text"}]` {
		t.Fatal(got)
	}
	if got := sys(`{"system":"base"}`, config.Route{SystemMode: config.SystemReplace, SystemPrompt: "only"}); got != `[{"text":"only","type":"text"}]` {
		t.Fatal(got)
	}
	// Before: replace with an empty prompt sent [{"type":"text","text":""}].
	if got := sys(`{"system":"base"}`, config.Route{SystemMode: config.SystemReplace}); got != "null" {
		t.Fatalf("empty replace must drop system: %s", got)
	}
	if got := sys(`{}`, config.Route{SystemMode: config.SystemAppend, SystemPrompt: "x"}); got != `[{"text":"x","type":"text"}]` {
		t.Fatal(got)
	}
}
