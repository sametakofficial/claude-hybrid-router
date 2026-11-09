package openaichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
)

func run(t *testing.T, src string) (string, []event, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := NewStream(&buf, nil, "claude-x", 42).Run(strings.NewReader(src))
	return buf.String(), parseSSE(t, buf.String()), err
}

func TestStreamReasoningBecomesThinking(t *testing.T) {
	src := `data: {"choices":[{"delta":{"reasoning_content":"let me "}}]}` + "\n" +
		`data: {"choices":[{"delta":{"reasoning_content":"think"}}]}` + "\n" +
		`data: {"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}` + "\n" + "data: [DONE]\n"
	out, evs, err := run(t, src)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range evs {
		names = append(names, e.name)
	}
	want := "message_start,content_block_start,content_block_delta,content_block_delta,content_block_delta,content_block_stop,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop"
	if strings.Join(names, ",") != want {
		t.Fatalf("events %s\n%s", strings.Join(names, ","), out)
	}
	if evs[1].data["content_block"].(map[string]any)["type"] != "thinking" || evs[2].data["delta"].(map[string]any)["thinking"] != "let me " {
		t.Fatalf("thinking block: %s", out)
	}
	if sig := evs[4].data["delta"].(map[string]any)["signature"]; sig != anthropic.SignaturePrefix+"reasoning_content" {
		t.Fatalf("signature %v", sig)
	}
	// A-M10: message_start reports the input estimate instead of 0.
	if evs[0].data["message"].(map[string]any)["usage"].(map[string]any)["input_tokens"].(float64) != 42 {
		t.Fatalf("message_start usage: %v", evs[0].data)
	}
}

func TestReasoningRoundTrip(t *testing.T) {
	out, _, err := FromOpenAI([]byte(`{"choices":[{"message":{"reasoning":"hmm","content":"hi"},"finish_reason":"stop"}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Content []map[string]any `json:"content"`
	}
	json.Unmarshal(out, &msg)
	if len(msg.Content) != 2 || msg.Content[0]["type"] != "thinking" || msg.Content[0]["signature"] != anthropic.SignaturePrefix+"reasoning" {
		t.Fatalf("%s", out)
	}
	// Claude Code sends the turn back: the reasoning returns in the same
	// field; a real Anthropic thinking block is dropped.
	contentJSON, _ := json.Marshal(append(msg.Content, map[string]any{"type": "thinking", "thinking": "real", "signature": "EqQBCkYIBxgCKkA"}))
	req := `{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","content":` + string(contentJSON) + `},{"role":"user","content":"next"}]}`
	b, err := ToOpenAI([]byte(req), Options{})
	if err != nil {
		t.Fatal(err)
	}
	asst := decodeMap(t, b)["messages"].([]any)[1].(map[string]any)
	if asst["reasoning"] != "hmm" || asst["content"] != "hi" || strings.Contains(string(b), "real") {
		t.Fatalf("assistant: %s", b)
	}
}

func TestStreamCutOffIsAnError(t *testing.T) {
	// A-M1: before, this was reported as a complete end_turn answer.
	out, _, err := run(t, `data: {"choices":[{"delta":{"content":"half an ans"}}]}`+"\n")
	if err == nil || !strings.Contains(out, "event: error") || strings.Contains(out, "end_turn") {
		t.Fatalf("err=%v\n%s", err, out)
	}
}

func TestStreamErrorAsString(t *testing.T) {
	// A-M4: {"error":"..."} used to fail to parse and be skipped.
	out, _, err := run(t, `data: {"error":"model overloaded"}`+"\n")
	if err == nil || !strings.Contains(out, "model overloaded") {
		t.Fatalf("err=%v\n%s", err, out)
	}
}

func TestToolArgumentsAsObjectAndUniqueIDs(t *testing.T) {
	// A-M4: arguments given as an object; A-M5: no ids.
	src := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"A","arguments":{"x":1}}},{"index":1,"function":{"name":"B","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\ndata: [DONE]\n"
	out, evs, err := run(t, src)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range evs {
		if cb, ok := e.data["content_block"].(map[string]any); ok && cb["type"] == "tool_use" {
			ids = append(ids, cb["id"].(string))
		}
		if d, ok := e.data["delta"].(map[string]any); ok && d["type"] == "input_json_delta" && d["partial_json"] == `{"x":1}` {
			ids = append(ids, "args-ok")
		}
	}
	if len(ids) != 3 || ids[0] == ids[2] || !strings.HasPrefix(ids[0], "toolu_") || ids[1] != "args-ok" {
		t.Fatalf("ids %v\n%s", ids, out)
	}
	// Two runs must not reuse ids either (before: toolu_chr_0 every turn).
	_, evs2, _ := run(t, src)
	if evs2[1].data["content_block"].(map[string]any)["id"] == ids[0] {
		t.Fatal("ids repeat across turns")
	}
	b, _, _ := FromOpenAI([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"A","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`), "m")
	if strings.Contains(string(b), `"id":""`) {
		t.Fatalf("empty tool id: %s", b)
	}
}

func TestRunJSONPrettyError(t *testing.T) {
	// A-M2: a 200 reply with a pretty-printed error lost the message.
	var buf bytes.Buffer
	_, err := NewStream(&buf, nil, "m", 0).RunJSON([]byte("{\n  \"error\": {\n    \"message\": \"quota exceeded\"\n  }\n}"))
	if err == nil || !strings.Contains(buf.String(), "quota exceeded") {
		t.Fatalf("%v %s", err, buf.String())
	}
	buf.Reset()
	if _, err := NewStream(&buf, nil, "m", 0).RunJSON([]byte(`{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`)); err != nil || !strings.Contains(buf.String(), "hi") {
		t.Fatalf("%v %s", err, buf.String())
	}
}

func TestServerToolUseIsDropped(t *testing.T) {
	// A-M6: server_tool_use became a tool call with no tool reply -> 400.
	req := `{"model":"m","messages":[{"role":"user","content":"search"},{"role":"assistant","content":[
		{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"x"}},
		{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]},
		{"type":"text","text":"found it"}]},{"role":"user","content":"thanks"}]}`
	b, err := ToOpenAI([]byte(req), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "tool_calls") || strings.Contains(string(b), "srvtoolu") || !strings.Contains(string(b), "found it") {
		t.Fatalf("%s", b)
	}
}
