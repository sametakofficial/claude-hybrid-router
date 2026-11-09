package anthropic

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEstimateCountsImagesAsFixed(t *testing.T) {
	img := strings.Repeat("A", 1_500_000) // a 1.5 MB screenshot
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + img + `"}}]}]}`
	n := EstimateTokens([]byte(body))
	// Before: len(body)/4 ≈ 375k tokens.
	if n < imageTokens || n > imageTokens+200 {
		t.Fatalf("estimate %d, want about %d", n, imageTokens)
	}
	if n := EstimateTokens([]byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("word ", 400) + `"}]}`)); n < 450 || n > 600 {
		t.Fatalf("text estimate %d", n)
	}
	if EstimateTokens([]byte("not json")) < 1 {
		t.Fatal("fallback")
	}
}

func TestUsageWriterJSONAndSSE(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewUsageWriter(rec)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"usage":{"input_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens":4}}`))
	if u := w.Usage(); u != (Usage{Input: 6, Output: 4, CacheRead: 2, CacheWrite: 1}) || w.Status() != 200 {
		t.Fatalf("json usage %+v", u)
	}
	rec = httptest.NewRecorder()
	w = NewUsageWriter(rec)
	w.Header().Set("Content-Type", "text/event-stream")
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n"
	for _, part := range []string{stream[:30], stream[30:]} { // split mid-line
		w.Write([]byte(part))
	}
	if u := w.Usage(); u.Input != 10 || u.Output != 7 {
		t.Fatalf("sse usage %+v", u)
	}
	if rec.Body.String() != stream {
		t.Fatal("bytes must pass through unchanged")
	}
	// SSE without the right Content-Type is still recognised.
	rec = httptest.NewRecorder()
	w = NewUsageWriter(rec)
	w.Write([]byte(stream))
	if u := w.Usage(); u.Output != 7 {
		t.Fatalf("sniffed sse usage %+v", u)
	}
}

func TestErrorsAndSSE(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, 429, "slow down")
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), `"rate_limit_error"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var buf bytes.Buffer
	flushes := 0
	s := NewSSE(&buf, func() { flushes++ })
	s.Event("message_start", MessageStart("msg_1", "m", 12))
	s.Ping()
	if flushes != 2 || !strings.Contains(buf.String(), `"input_tokens":12`) || !strings.Contains(buf.String(), "event: ping\n") {
		t.Fatalf("%d %s", flushes, buf.String())
	}
	r, err := ParseRequest([]byte(`{"model":"x","max_tokens":12345678901,"stream":true,"system":[{"type":"text","text":"a"}]}`))
	if err != nil || r.Model() != "x" || !r.Stream() {
		t.Fatal(err)
	}
	if n, ok := r.Int("max_tokens"); !ok || n != 12345678901 {
		t.Fatalf("int %d", n)
	}
	r.EditSystem(strings.ToUpper)
	if r["system"].([]any)[0].(map[string]any)["text"] != "A" {
		t.Fatal("edit system")
	}
}
