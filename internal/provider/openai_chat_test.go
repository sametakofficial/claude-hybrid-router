package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func TestOpenAIAdapterSniffsTheBody(t *testing.T) {
	mode := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-p" || r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"bad key sk-abcdefghijklmn"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json") // wrong for SSE on purpose
		switch mode {
		case "sse":
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" second\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			io.WriteString(w, `{"choices":[{"message":{"content":"whole"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`)
		}
	}))
	defer srv.Close()
	ad := newOpenAI(config.Provider{ID: "gpt", BaseURL: srv.URL + "/v1", APIKey: "sk-p"}, NewClient(NewTransport(time.Second)))
	send := func(body string) string {
		reply, err := ad.Messages(context.Background(), &Request{Body: parse(t, body), ShownModel: "claude-x"})
		if err != nil {
			t.Fatal(err)
		}
		defer reply.Close()
		rec := httptest.NewRecorder()
		reply.WriteTo(rec)
		return rec.Body.String()
	}
	// A-M3: SSE with a JSON Content-Type; before, the first chunk was lost.
	mode = "sse"
	if out := send(`{"model":"m","stream":true,"messages":[{"role":"user","content":"x"}]}`); !strings.Contains(out, `"first"`) || !strings.Contains(out, "message_stop") {
		t.Fatalf("sse: %s", out)
	}
	// stream asked, provider sent one JSON object.
	mode = "json"
	if out := send(`{"model":"m","stream":true,"messages":[{"role":"user","content":"x"}]}`); !strings.Contains(out, "event: message_start") || !strings.Contains(out, "whole") {
		t.Fatalf("json as stream: %s", out)
	}
	if out := send(`{"model":"m","messages":[{"role":"user","content":"x"}]}`); !strings.Contains(out, `"type":"message"`) || !strings.Contains(out, `"claude-x"`) {
		t.Fatalf("json: %s", out)
	}
	// no stream asked, provider streamed anyway: collected into one message.
	mode = "sse"
	if out := send(`{"model":"m","messages":[{"role":"user","content":"x"}]}`); !strings.Contains(out, `"text":"first second"`) || !strings.Contains(out, `"end_turn"`) {
		t.Fatalf("sse collected: %s", out)
	}
	bad := newOpenAI(config.Provider{ID: "gpt", BaseURL: srv.URL + "/v1", APIKey: "wrong"}, NewClient(NewTransport(time.Second)))
	_, err := bad.Messages(context.Background(), &Request{Body: parse(t, `{"model":"m","messages":[]}`)})
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 401 || strings.Contains(ue.Message, "abcdefghijklmn") {
		t.Fatalf("want redacted UpstreamError, got %v", err)
	}
}
