package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/translate/openaichat"
)

const maxReply = 64 << 20

// openAIAdapter translates to an OpenAI-compatible /chat/completions.
type openAIAdapter struct {
	p config.Provider
	c *Client
}

func newOpenAI(p config.Provider, c *Client) Adapter { return &openAIAdapter{p: p, c: c} }

func (a *openAIAdapter) base() string { return strings.TrimRight(a.p.BaseURL, "/") }

func (a *openAIAdapter) header() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if key := a.p.Key(); key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	setHeaders(h, a.p)
	return h
}

func (a *openAIAdapter) Messages(ctx context.Context, req *Request) (Reply, error) {
	raw, err := json.Marshal(req.Body)
	if err != nil {
		return nil, err
	}
	rt := req.Route
	body, err := openaichat.ToOpenAI(raw, openaichat.Options{
		Model: rt.Model, MaxTokensCap: rt.MaxTokens, MaxTokensField: a.p.MaxTokensField,
		Temperature: rt.Temperature, ReasoningEffort: rt.ReasoningEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}
	resp, err := a.c.Do(ctx, func() (*http.Request, error) {
		out, err := http.NewRequest(http.MethodPost, a.base()+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		out.Header = a.header()
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, ReadError(a.p.ID, resp)
	}
	return &openAIReply{
		id: a.p.ID, resp: resp, stream: req.Body.Stream(),
		model: req.ShownModel, inputGuess: anthropic.EstimateTokens(raw),
	}, nil
}

// CountTokens is an estimate: Chat Completions has no count endpoint.
func (a *openAIAdapter) CountTokens(_ context.Context, req *Request) (int, error) {
	raw, err := json.Marshal(req.Body)
	if err != nil {
		return 0, err
	}
	return anthropic.EstimateTokens(raw), nil
}

func (a *openAIAdapter) ListModels(ctx context.Context) ([]string, error) {
	h := a.header()
	h.Del("Content-Type")
	return listModels(ctx, a.c, a.base()+"/models", h)
}

type openAIReply struct {
	id         string
	resp       *http.Response
	stream     bool
	model      string // the model name shown to Claude Code
	inputGuess int
}

func (r *openAIReply) Close() error { return r.resp.Body.Close() }

// WriteTo converts the reply. Whether the provider streamed is read from
// the body itself: some servers send SSE with a JSON Content-Type, others
// ignore stream=true and send one JSON object.
func (r *openAIReply) WriteTo(w http.ResponseWriter) (anthropic.Usage, error) {
	br := bufio.NewReaderSize(r.resp.Body, 64<<10)
	isSSE := sniffSSE(br)
	if r.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		st := openaichat.NewStream(w, func() {
			if f != nil {
				f.Flush()
			}
		}, r.model, r.inputGuess)
		var u openaichat.Usage
		var err error
		if isSSE {
			u, err = st.Run(br)
		} else {
			data, rerr := io.ReadAll(io.LimitReader(br, maxReply))
			if rerr != nil {
				return anthropic.Usage{}, rerr
			}
			u, err = st.RunJSON(data)
		}
		return u.Wire(), err
	}
	if isSSE {
		err := fmt.Errorf("provider %q streamed although no stream was asked for", r.id)
		anthropic.WriteError(w, http.StatusBadGateway, "claude-hybrid: "+err.Error())
		return anthropic.Usage{}, err
	}
	data, err := io.ReadAll(io.LimitReader(br, maxReply))
	if err != nil {
		anthropic.WriteError(w, http.StatusBadGateway, "claude-hybrid: read provider response: "+err.Error())
		return anthropic.Usage{}, err
	}
	out, u, err := openaichat.FromOpenAI(data, r.model)
	if err != nil {
		anthropic.WriteError(w, http.StatusBadGateway, fmt.Sprintf("claude-hybrid: provider %q: %v", r.id, Redact(err.Error())))
		return anthropic.Usage{}, err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
	return u.Wire(), nil
}

// sniffSSE peeks at the start of the body: SSE starts with a field name
// ("data:", "event:", ...) or a comment (":").
func sniffSSE(br *bufio.Reader) bool {
	head, _ := br.Peek(512)
	head = bytes.TrimLeft(head, " \t\r\n")
	for _, p := range []string{"data:", "event:", ":", "id:", "retry:"} {
		if bytes.HasPrefix(head, []byte(p)) {
			return true
		}
	}
	return false
}
