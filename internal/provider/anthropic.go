package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

const defaultVersion = "2023-06-01"

// anthropicAdapter forwards to an Anthropic-compatible /v1/messages. The
// client's headers are NOT copied, so the subscription token never reaches
// a third party; the provider's own key is sent instead.
type anthropicAdapter struct {
	p config.Provider
	c *Client
}

func newAnthropic(p config.Provider, c *Client) Adapter { return &anthropicAdapter{p: p, c: c} }

func (a *anthropicAdapter) base() string {
	return strings.TrimSuffix(strings.TrimRight(a.p.BaseURL, "/"), "/v1")
}

func (a *anthropicAdapter) header(version string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if version == "" {
		version = defaultVersion
	}
	h.Set("anthropic-version", version)
	if key := a.p.Key(); key != "" {
		h.Set("x-api-key", key)
		h.Set("Authorization", "Bearer "+key)
	}
	setHeaders(h, a.p)
	return h
}

func (a *anthropicAdapter) post(ctx context.Context, path string, req *Request, body []byte) (*http.Response, error) {
	target := a.base() + path
	if req.Query != "" {
		target += "?" + req.Query
	}
	return a.c.Do(ctx, func() (*http.Request, error) {
		out, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		out.Header = a.header(req.Version)
		return out, nil
	})
}

func (a *anthropicAdapter) Messages(ctx context.Context, req *Request) (Reply, error) {
	body, err := json.Marshal(PrepareAnthropic(req.Body, req.Route))
	if err != nil {
		return nil, err
	}
	resp, err := a.post(ctx, "/v1/messages", req, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, ReadError(a.p.ID, resp)
	}
	return passReply{resp}, nil
}

func (a *anthropicAdapter) CountTokens(ctx context.Context, req *Request) (int, error) {
	body, err := json.Marshal(PrepareAnthropic(req.Body, req.Route))
	if err != nil {
		return 0, err
	}
	estimate := anthropic.EstimateTokens(body)
	// count_tokens has no max_tokens/stream/thinking budget semantics.
	var probe map[string]any
	json.Unmarshal(body, &probe)
	for _, k := range []string{"max_tokens", "stream", "temperature", "top_p", "top_k", "stop_sequences", "metadata", "service_tier"} {
		delete(probe, k)
	}
	body, _ = json.Marshal(probe)
	resp, err := a.post(ctx, "/v1/messages/count_tokens", req, body)
	if err != nil {
		return estimate, nil
	}
	defer resp.Body.Close()
	var out struct {
		InputTokens *int `json:"input_tokens"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &out) != nil || out.InputTokens == nil {
		return estimate, nil // many compatible APIs lack count_tokens
	}
	return *out.InputTokens, nil
}

func (a *anthropicAdapter) ListModels(ctx context.Context) ([]string, error) {
	h := a.header("")
	h.Del("Content-Type")
	return listModels(ctx, a.c, a.base()+"/v1/models", h)
}

// passReply copies an Anthropic-format reply through unchanged.
type passReply struct{ resp *http.Response }

func (r passReply) Close() error { return r.resp.Body.Close() }

func (r passReply) WriteTo(w http.ResponseWriter) (anthropic.Usage, error) {
	uw := anthropic.NewUsageWriter(w)
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := r.resp.Header.Get(k); v != "" {
			uw.Header().Set(k, v)
		}
	}
	uw.WriteHeader(http.StatusOK)
	_, err := CopyFlush(uw, r.resp.Body)
	return uw.Usage(), err
}

// CopyFlush copies src to dst, flushing after every read so SSE stays live.
func CopyFlush(dst io.Writer, src io.Reader) (int64, error) {
	f, _ := dst.(http.Flusher)
	buf := make([]byte, 32<<10)
	var n int64
	for {
		k, err := src.Read(buf)
		if k > 0 {
			if _, werr := dst.Write(buf[:k]); werr != nil {
				return n, werr
			}
			n += int64(k)
			if f != nil {
				f.Flush()
			}
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// standardFields are the Messages API request fields every compatible
// provider is expected to know. Everything else (context_management,
// container, mcp_servers, output_config, ...) needs an anthropic-beta header,
// which is not forwarded, so strict providers would reject the request.
var standardFields = map[string]bool{
	"model": true, "messages": true, "system": true, "max_tokens": true, "metadata": true,
	"stop_sequences": true, "stream": true, "temperature": true, "top_p": true, "top_k": true,
	"tools": true, "tool_choice": true, "thinking": true, "service_tier": true,
}

// PrepareAnthropic applies the route overrides to a request for an
// Anthropic-compatible provider and removes beta-only parts.
func PrepareAnthropic(body anthropic.Request, rt config.Route) anthropic.Request {
	if rt.Model != "" {
		body["model"] = rt.Model
	}
	if rt.MaxTokens > 0 {
		if n, ok := body.Int("max_tokens"); !ok || n > rt.MaxTokens {
			body["max_tokens"] = rt.MaxTokens
		}
	}
	if rt.Temperature != nil {
		body["temperature"] = *rt.Temperature
	}
	for k := range body {
		if !standardFields[k] {
			delete(body, k)
		}
	}
	clampThinking(body)
	stripBeta(body)
	return body
}

// minThinkingBudget is the smallest budget_tokens the API accepts.
const minThinkingBudget = 1024

// clampThinking keeps thinking.budget_tokens below max_tokens (the API
// requires it). When a max_tokens cap leaves no room, half the cap goes to
// thinking; below the minimum budget thinking is turned off.
func clampThinking(body anthropic.Request) {
	th, ok := body["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" {
		return
	}
	maxTokens, ok := body.Int("max_tokens")
	if !ok {
		return
	}
	budget, ok := anthropic.Request(th).Int("budget_tokens")
	if !ok || budget < maxTokens {
		return
	}
	if half := maxTokens / 2; half >= minThinkingBudget {
		th["budget_tokens"] = half
		return
	}
	delete(body, "thinking")
}

// stripBeta removes beta-only keys inside the standard fields: the 1h cache
// TTL, unknown metadata keys, and beta tool options.
func stripBeta(body anthropic.Request) {
	if md, ok := body["metadata"].(map[string]any); ok {
		for k := range md {
			if k != "user_id" {
				delete(md, k)
			}
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		for _, t := range tools {
			if m, ok := t.(map[string]any); ok {
				if typ, _ := m["type"].(string); typ == "" || typ == "custom" {
					for k := range m {
						switch k {
						case "type", "name", "description", "input_schema", "cache_control":
						default:
							delete(m, k) // defer_loading, input_examples, allowed_callers, ...
						}
					}
				}
			}
		}
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if cc, ok := t["cache_control"].(map[string]any); ok {
				delete(cc, "ttl")
			}
			for _, v := range t {
				walk(v)
			}
		case []any:
			for _, v := range t {
				walk(v)
			}
		}
	}
	walk(body["system"])
	walk(body["messages"])
	walk(body["tools"])
}
