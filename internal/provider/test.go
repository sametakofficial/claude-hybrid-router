package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

// Test sends a tiny prompt through an adapter, the same path real requests
// take, and returns the reply text (the panel's Test button).
func Test(ctx context.Context, ad Adapter, model string) (string, time.Duration, error) {
	body, _ := anthropic.ParseRequest([]byte(`{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"Reply with the single word OK."}]}`))
	start := time.Now()
	reply, err := ad.Messages(ctx, &Request{Body: body, Route: config.Route{Model: model}, ShownModel: "claude-test"})
	if err != nil {
		return "", time.Since(start), err
	}
	defer reply.Close()
	w := &bufferWriter{header: http.Header{}}
	_, err = reply.WriteTo(w)
	took := time.Since(start)
	if err != nil {
		return "", took, err
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.buf.Bytes(), &out); err != nil {
		return "", took, fmt.Errorf("unexpected response: %.300s", w.buf.String())
	}
	if out.Error != nil {
		return "", took, fmt.Errorf("%s", out.Error.Message)
	}
	var sb strings.Builder
	for _, c := range out.Content {
		sb.WriteString(c.Text)
	}
	return sb.String(), took, nil
}

// bufferWriter is a minimal in-memory http.ResponseWriter.
type bufferWriter struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (b *bufferWriter) Header() http.Header         { return b.header }
func (b *bufferWriter) WriteHeader(code int)        { b.code = code }
func (b *bufferWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
