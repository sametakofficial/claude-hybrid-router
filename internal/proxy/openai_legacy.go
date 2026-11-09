package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/provider"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
	"github.com/sametakofficial/claude-hybrid-router/internal/translate"
)

// Old OpenAI path; replaced by provider/openai_chat.go in the next step.

// toOpenAI translates to Chat Completions and back.
func (p *Proxy) toOpenAI(w http.ResponseWriter, r *http.Request, prov config.Provider, rt config.Route, req anthropic.Request, e stats.Entry, countOnly bool) {
	raw, err := json.Marshal(req)
	if err != nil {
		p.fail(w, e, http.StatusBadRequest, "claude-hybrid: encode request: "+err.Error(), countOnly)
		return
	}
	if countOnly {
		anthropic.WriteCount(w, anthropic.EstimateTokens(raw))
		return
	}
	body, err := translate.ToOpenAI(raw, translate.Options{
		Model: rt.Model, MaxTokensCap: rt.MaxTokens, MaxTokensField: prov.MaxTokensField,
		Temperature: rt.Temperature, ReasoningEffort: rt.ReasoningEffort,
	})
	if err != nil {
		p.fail(w, e, http.StatusBadRequest, "claude-hybrid: translate request: "+err.Error(), false)
		return
	}
	target := strings.TrimRight(prov.BaseURL, "/") + "/chat/completions"
	start := time.Now()
	resp, err := p.client.Do(r.Context(), func() (*http.Request, error) {
		out, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		out.Header.Set("Content-Type", "application/json")
		if key := prov.Key(); key != "" {
			out.Header.Set("Authorization", "Bearer "+key)
		}
		setProviderHeaders(out.Header, prov)
		return out, nil
	})
	if err != nil {
		p.fail(w, e, http.StatusBadGateway, fmt.Sprintf("claude-hybrid: provider %q unreachable: %v", prov.ID, err), false)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.providerError(w, e, prov, resp, false)
		return
	}
	// The model name shown to Claude Code: keep what it asked for so its
	// own bookkeeping (context window, pricing labels) stays sane.
	shownModel := e.ReqModel
	isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	if e.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		src := io.Reader(resp.Body)
		if !isSSE { // provider ignored stream=true: wrap the JSON as one chunk
			src = jsonAsSSE(resp.Body)
		}
		usage, err := translate.NewStream(w, flush, shownModel).Run(src)
		e.Status, e.LatencyMS = 200, time.Since(start).Milliseconds()
		e.InputTokens, e.OutputTokens = usage.InputTokens+usage.CacheRead, usage.OutputTokens
		e.CacheReadTokens = usage.CacheRead
		if err != nil && r.Context().Err() == nil {
			e.Error = err.Error()
		}
		p.rec.Add(e)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		p.fail(w, e, http.StatusBadGateway, "claude-hybrid: read provider response: "+err.Error(), false)
		return
	}
	out, usage, err := translate.FromOpenAI(data, shownModel)
	if err != nil {
		p.fail(w, e, http.StatusBadGateway, fmt.Sprintf("claude-hybrid: provider %q: %v", prov.ID, err), false)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
	e.Status, e.LatencyMS = 200, time.Since(start).Milliseconds()
	e.InputTokens, e.OutputTokens = usage.InputTokens+usage.CacheRead, usage.OutputTokens
	e.CacheReadTokens = usage.CacheRead
	p.rec.Add(e)
}

// providerError turns a provider failure into an Anthropic-style error.
// 401/403 are mapped to 400: passing them on would make Claude Code think
// the user's own Anthropic login expired.
func (p *Proxy) providerError(w http.ResponseWriter, e stats.Entry, prov config.Provider, resp *http.Response, countOnly bool) {
	p.providerFailure(w, e, prov, provider.ReadError(prov.ID, resp))
}

// jsonAsSSE wraps a non-streaming Chat Completions JSON reply as a stream of
// one chunk, for providers that ignore stream=true.
func jsonAsSSE(r io.Reader) io.Reader {
	data, _ := io.ReadAll(io.LimitReader(r, maxBody))
	var full struct {
		ID      string `json:"id"`
		Choices []struct {
			Message      json.RawMessage `json:"message"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(data, &full) != nil || len(full.Choices) == 0 {
		return bytes.NewReader(append(append([]byte("data: "), data...), "\n\ndata: [DONE]\n\n"...))
	}
	chunk := map[string]any{
		"id":      full.ID,
		"choices": []any{map[string]any{"delta": full.Choices[0].Message, "finish_reason": full.Choices[0].FinishReason}},
	}
	if len(full.Usage) > 0 {
		chunk["usage"] = full.Usage
	}
	b, _ := json.Marshal(chunk)
	return strings.NewReader("data: " + string(b) + "\n\ndata: [DONE]\n\n")
}

func setProviderHeaders(h http.Header, prov config.Provider) {
	for k, v := range prov.Headers {
		h.Set(k, v)
	}
}
