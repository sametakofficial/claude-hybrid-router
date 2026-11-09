// Package provider talks to model providers. Each provider type has an
// Adapter that turns an Anthropic Messages request into the provider's wire
// format and the provider's reply back into an Anthropic reply.
//
// Sending is split from writing: Messages returns once the provider has
// answered with headers (or failed), and only Reply.WriteTo touches the
// client connection. So the proxy can apply one policy for every adapter:
// retries happen before any byte reaches Claude Code, a provider 401/403 is
// never shown to Claude Code as 401/403, and streams are flushed.
package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

// Request is one routed Messages request.
type Request struct {
	Body       anthropic.Request // Anthropic request, markers removed and system_mode applied; the adapter may change it
	Route      config.Route      // model, max_tokens, temperature, reasoning overrides
	Query      string            // raw query of the client request (e.g. "beta=true")
	Version    string            // client's anthropic-version header ("" = default)
	ShownModel string            // model name to report back to Claude Code
}

// Reply is a provider answer that has not been sent to the client yet.
type Reply interface {
	// WriteTo writes the reply as an Anthropic response (JSON, or SSE when
	// the request asked for a stream) and returns the token usage. An error
	// after the first byte is reported inside the stream where possible.
	WriteTo(w http.ResponseWriter) (anthropic.Usage, error)
	// Close releases the provider connection. Safe after WriteTo.
	Close() error
}

// Adapter is one provider type bound to one configured provider.
type Adapter interface {
	// Messages sends the request. An HTTP error from the provider is an
	// *UpstreamError; anything else is a transport error.
	Messages(ctx context.Context, req *Request) (Reply, error)
	// CountTokens answers count_tokens (often an estimate).
	CountTokens(ctx context.Context, req *Request) (int, error)
	// ListModels reads the provider's model list.
	ListModels(ctx context.Context) ([]string, error)
}

// UpstreamError is an HTTP error answer from a provider.
type UpstreamError struct {
	Provider string
	Status   int
	Message  string // redacted, shortened body
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("provider %q returned HTTP %d: %s", e.Provider, e.Status, e.Message)
}

// ReadError reads a failed provider response into an UpstreamError.
func ReadError(id string, resp *http.Response) *UpstreamError {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := Redact(strings.TrimSpace(string(data)))
	if len(msg) > 1500 {
		msg = msg[:1500] + "…"
	}
	return &UpstreamError{Provider: id, Status: resp.StatusCode, Message: msg}
}

var redactRE = regexp.MustCompile(`(?i)(bearer\s+|sk-|key-)[A-Za-z0-9._\-]{8,}`)

// Redact hides things that look like API keys in provider error text.
func Redact(s string) string { return redactRE.ReplaceAllString(s, "${1}[REDACTED]") }

// setHeaders adds the provider's custom headers.
func setHeaders(h http.Header, p config.Provider) {
	for k, v := range p.Headers {
		h.Set(k, v)
	}
}
