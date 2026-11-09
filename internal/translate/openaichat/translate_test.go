package openaichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func decodeMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad json %s: %v", b, err)
	}
	return m
}

func TestToOpenAIFullConversation(t *testing.T) {
	in := `{
	  "model":"claude-x","max_tokens":32000,"stream":true,"temperature":1,
	  "system":[{"type":"text","text":"sys one","cache_control":{"type":"ephemeral"}},{"type":"text","text":"sys two"}],
	  "tools":[{"name":"Read","description":"read a file","input_schema":{"$schema":"x","type":"object","properties":{"path":{"type":"string"}}}},
	           {"type":"web_search_20250305","name":"web_search"}],
	  "tool_choice":{"type":"any","disable_parallel_tool_use":true},
	  "messages":[
	    {"role":"user","content":"hello"},
	    {"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"reading"},{"type":"tool_use","id":"t1","name":"Read","input":{"path":"a"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"file body"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]},{"type":"text","text":"go on"}]}
	  ]}`
	out, err := ToOpenAI([]byte(in), Options{Model: "gpt-x", MaxTokensCap: 8000, MaxTokensField: "max_completion_tokens", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	m := decodeMap(t, out)
	if m["model"] != "gpt-x" || m["max_completion_tokens"].(float64) != 8000 || m["reasoning_effort"] != "high" {
		t.Fatalf("options not applied: %s", out)
	}
	if m["tool_choice"] != "required" || m["parallel_tool_calls"] != false {
		t.Fatalf("tool choice: %s", out)
	}
	tools := m["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("server tool should be dropped: %s", out)
	}
	params := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if _, ok := params["$schema"]; ok {
		t.Fatal("$schema should be removed")
	}
	msgs := m["messages"].([]any)
	roles := []string{}
	for _, x := range msgs {
		roles = append(roles, x.(map[string]any)["role"].(string))
	}
	if got := strings.Join(roles, ","); got != "system,user,assistant,tool,user" {
		t.Fatalf("roles = %s\n%s", got, out)
	}
	if msgs[0].(map[string]any)["content"] != "sys one\n\nsys two" {
		t.Fatalf("system: %v", msgs[0])
	}
	asst := msgs[2].(map[string]any)
	if asst["content"] != "reading" {
		t.Fatalf("assistant content: %v", asst)
	}
	call := asst["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "t1" || call["function"].(map[string]any)["arguments"] != `{"path":"a"}` {
		t.Fatalf("tool call: %v", call)
	}
	tool := msgs[3].(map[string]any)
	if tool["tool_call_id"] != "t1" || tool["content"] != "file body" {
		t.Fatalf("tool msg: %v", tool)
	}
	last := msgs[4].(map[string]any)["content"].([]any)
	if len(last) != 3 || last[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("tool image should move to user message: %v", last)
	}
	if so := m["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Fatal("stream_options missing")
	}
}

func TestToOpenAIKeepsSmallerMaxTokens(t *testing.T) {
	out, _ := ToOpenAI([]byte(`{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"x"}]}`), Options{MaxTokensCap: 8000})
	if decodeMap(t, out)["max_tokens"].(float64) != 100 {
		t.Fatalf("got %s", out)
	}
}

func TestFromOpenAI(t *testing.T) {
	in := `{"id":"chatcmpl-9","choices":[{"message":{"content":"hi","tool_calls":[{"id":"c1","function":{"name":"Bash","arguments":"{\"cmd\":\"ls\"}"}},{"id":"c2","function":{"name":"Bad","arguments":"{oops"}}]},"finish_reason":"tool_calls"}],
	        "usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40}}}`
	out, u, err := FromOpenAI([]byte(in), "claude-x")
	if err != nil {
		t.Fatal(err)
	}
	m := decodeMap(t, out)
	if m["stop_reason"] != "tool_use" || m["id"] != "msg_9" || m["model"] != "claude-x" {
		t.Fatalf("meta: %s", out)
	}
	content := m["content"].([]any)
	if len(content) != 3 || content[1].(map[string]any)["input"].(map[string]any)["cmd"] != "ls" {
		t.Fatalf("content: %s", out)
	}
	if content[2].(map[string]any)["input"].(map[string]any)["_raw_arguments"] != "{oops" {
		t.Fatalf("broken args should be kept: %s", out)
	}
	if u.InputTokens != 60 || u.CacheRead != 40 || u.OutputTokens != 7 {
		t.Fatalf("usage: %+v", u)
	}
}

func TestFromOpenAIError(t *testing.T) {
	if _, _, err := FromOpenAI([]byte(`{"error":{"message":"nope"}}`), "m"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

type event struct {
	name string
	data map[string]any
}

func parseSSE(t *testing.T, s string) []event {
	t.Helper()
	var out []event
	for _, chunk := range strings.Split(strings.TrimSpace(s), "\n\n") {
		lines := strings.SplitN(chunk, "\n", 2)
		if len(lines) != 2 {
			t.Fatalf("bad chunk %q", chunk)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &d); err != nil {
			t.Fatalf("bad data %q", lines[1])
		}
		out = append(out, event{strings.TrimPrefix(lines[0], "event: "), d})
	}
	return out
}

func TestStream(t *testing.T) {
	src := strings.Join([]string{
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"Hel"}}]}`,
		`: keep-alive`,
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"lo"}}]}`,
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"Read","arguments":""}}]}}]}`,
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"Grep","arguments":"{\"q\":"}}]}}]}`,
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"x\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"y\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`, ``,
	}, "\n")
	var buf bytes.Buffer
	u, err := NewStream(&buf, nil, "claude-x", 0).Run(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	evs := parseSSE(t, buf.String())
	names := []string{}
	for _, e := range evs {
		names = append(names, e.name)
	}
	want := "message_start,content_block_start,content_block_delta,content_block_delta,content_block_stop," +
		"content_block_start,content_block_delta,content_block_stop,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if evs[6].data["delta"].(map[string]any)["partial_json"] != `{"path":"x"}` || evs[9].data["delta"].(map[string]any)["partial_json"] != `{"q":"y"}` {
		t.Fatalf("tool args: %s", buf.String())
	}
	if evs[8].data["index"].(float64) != 2 {
		t.Fatal("block indexes must be sequential")
	}
	md := evs[11].data
	if md["delta"].(map[string]any)["stop_reason"] != "tool_use" || md["usage"].(map[string]any)["output_tokens"].(float64) != 5 {
		t.Fatalf("message_delta: %v", md)
	}
	if u.InputTokens != 10 {
		t.Fatalf("usage %+v", u)
	}
}

func TestStreamToolCallsWithoutIndex(t *testing.T) {
	src := `data: {"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"A","arguments":"{}"}},{"id":"b","function":{"name":"B","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n"
	var buf bytes.Buffer
	if _, err := NewStream(&buf, nil, "m", 0).Run(strings.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(buf.String(), `"type":"tool_use"`) != 2 {
		t.Fatalf("want 2 tool blocks:\n%s", buf.String())
	}
}

func TestStreamEmptyAndErrors(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewStream(&buf, nil, "m", 0).Run(strings.NewReader("")); err == nil || !strings.Contains(buf.String(), "event: error") {
		t.Fatalf("empty stream should send an error event, got %q", buf.String())
	}
	buf.Reset()
	if _, err := NewStream(&buf, nil, "m", 0).Run(strings.NewReader(`data: {"error":{"message":"quota"}}` + "\n")); err == nil || !strings.Contains(buf.String(), "quota") {
		t.Fatalf("provider error should be passed on, got %q", buf.String())
	}
	buf.Reset()
	// finish without any content still yields one (empty) text block
	if _, err := NewStream(&buf, nil, "m", 0).Run(strings.NewReader(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\ndata: [DONE]\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "content_block_start") || !strings.Contains(buf.String(), "end_turn") {
		t.Fatalf("got %q", buf.String())
	}
}
