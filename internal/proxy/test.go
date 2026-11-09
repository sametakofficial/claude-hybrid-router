package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
)

// Test sends a tiny prompt to a provider through the same code path that
// real requests use, and returns the reply text.
func (p *Proxy) Test(ctx context.Context, prov config.Provider, model string) (string, time.Duration, error) {
	body := `{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"Reply with the single word OK."}]}`
	req, err := anthropic.ParseRequest([]byte(body))
	if err != nil {
		return "", 0, err
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	rt := config.Route{Provider: prov.ID, Model: model}
	e := stats.Entry{Kind: "routed", Source: "test", Path: "/v1/messages", ReqModel: "claude-test", Provider: prov.ID, Model: model, Time: time.Now()}
	start := time.Now()
	if prov.Type == config.TypeAnthropic {
		p.toAnthropic(w, r, prov, rt, req, e, false)
	} else {
		p.toOpenAI(w, r, prov, rt, req, e, false)
	}
	took := time.Since(start)
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		return "", took, fmt.Errorf("unexpected response (HTTP %d): %.300s", w.Code, w.Body.String())
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
