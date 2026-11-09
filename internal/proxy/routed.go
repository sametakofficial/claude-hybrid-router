package proxy

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/anthropic"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
	"github.com/sametakofficial/claude-hybrid-router/internal/provider"
	"github.com/sametakofficial/claude-hybrid-router/internal/stats"
)

// routed sends a request to a provider through its adapter.
func (p *Proxy) routed(w http.ResponseWriter, r *http.Request, prov config.Provider, rt config.Route, req anthropic.Request, e stats.Entry, countOnly bool) {
	ad, err := provider.New(prov, p.client)
	if err != nil {
		p.fail(w, e, http.StatusBadRequest, "claude-hybrid: "+err.Error(), countOnly)
		return
	}
	preq := &provider.Request{Body: req, Route: rt, Query: r.URL.RawQuery, Version: r.Header.Get("anthropic-version"), ShownModel: e.ReqModel}
	if countOnly {
		n, _ := ad.CountTokens(r.Context(), preq)
		anthropic.WriteCount(w, n)
		return
	}
	start := time.Now()
	reply, err := ad.Messages(r.Context(), preq)
	if err != nil {
		e.LatencyMS = time.Since(start).Milliseconds()
		p.providerFailure(w, e, prov, err)
		return
	}
	defer reply.Close()
	u, err := reply.WriteTo(w)
	e.Status, e.LatencyMS = http.StatusOK, time.Since(start).Milliseconds()
	setUsage(&e, u)
	if err != nil && r.Context().Err() == nil {
		e.Error = err.Error()
	}
	p.rec.Add(e)
}

// providerFailure reports a provider that failed before sending anything.
// A provider 401/403 becomes 400: passing it on would make Claude Code
// think the user's own Anthropic login expired.
func (p *Proxy) providerFailure(w http.ResponseWriter, e stats.Entry, prov config.Provider, err error) {
	var ue *provider.UpstreamError
	if !errors.As(err, &ue) {
		p.fail(w, e, http.StatusBadGateway, fmt.Sprintf("claude-hybrid: provider %q unreachable: %v", prov.ID, err), false)
		return
	}
	code := ue.Status
	switch {
	case code == http.StatusTooManyRequests:
	case code >= 500:
		code = http.StatusBadGateway
	default:
		code = http.StatusBadRequest
	}
	msg := "claude-hybrid: " + ue.Error()
	if code == http.StatusBadRequest && contextOverflowRE.MatchString(ue.Message) {
		// Claude Code compacts the conversation when it sees this wording.
		msg = "prompt is too long: " + msg
	}
	log.Printf("provider error: %s", msg)
	p.fail(w, e, code, msg, false)
}

// contextOverflowRE matches how providers say the prompt does not fit.
var contextOverflowRE = regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded|maximum context length|context window|context length|prompt is too long|too many (input )?tokens|input is too long|reduce the length|exceeds? the (model's )?(maximum|max|context)|token limit`)
