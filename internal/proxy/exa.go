package proxy

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"
	"time"

	cfg "github.com/peter-wagstaff/claude-hybrid-router/internal/config"
)

type exaEnvelope struct {
	Provider         string              `json:"provider"`
	Mode             string              `json:"mode"`
	Query            string              `json:"query,omitempty"`
	Timestamp        string              `json:"timestamp"`
	FullTextIncluded bool                `json:"full_text_included"`
	FallbackReason   string              `json:"fallback_reason,omitempty"`
	Results          []exaEnvelopeResult `json:"results"`
}

type exaEnvelopeResult struct {
	ID            string               `json:"id,omitempty"`
	Title         string               `json:"title,omitempty"`
	URL           string               `json:"url,omitempty"`
	Domain        string               `json:"domain,omitempty"`
	PublishedDate string               `json:"published_date,omitempty"`
	Author        string               `json:"author,omitempty"`
	Summary       string               `json:"summary,omitempty"`
	Highlights    []string             `json:"highlights,omitempty"`
	Text          *string              `json:"text"`
	TextTruncated bool                 `json:"text_truncated"`
	SourceType    string               `json:"source_type"`
	Subpages      []exaEnvelopeSubpage `json:"subpages,omitempty"`
	Signals       exaEnvelopeSignals   `json:"signals"`
}

type exaEnvelopeSubpage struct {
	Title      string   `json:"title,omitempty"`
	URL        string   `json:"url,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Highlights []string `json:"highlights,omitempty"`
}

type exaEnvelopeSignals struct {
	HasSummary    bool `json:"has_summary"`
	HasHighlights bool `json:"has_highlights"`
	HasText       bool `json:"has_text"`
	LowSignal     bool `json:"low_signal"`
}

type exaSearchResponse struct {
	RequestID string            `json:"requestId"`
	Results   []exaSearchResult `json:"results"`
}

type exaSearchResult struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	URL            string            `json:"url"`
	PublishedDate  string            `json:"publishedDate"`
	Author         string            `json:"author"`
	Summary        string            `json:"summary"`
	Highlights     []string          `json:"highlights"`
	Subpages       []exaSubpage      `json:"subpages"`
	Text           string            `json:"text"`
	Status         string            `json:"status"`
	SummaryText    string            `json:"summaryText"`
	Extras         map[string]any    `json:"extras"`
	UnknownPayload map[string]any    `json:"-"`
}

type exaSubpage struct {
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Summary    string   `json:"summary"`
	Highlights []string `json:"highlights"`
}

func maybeNormalizeExaToolResult(toolOutput string, resolved cfg.ResolvedModel) string {
	if !isExaModel(resolved) {
		return toolOutput
	}
	env, ok := buildExaEnvelope(toolOutput, resolved.Exa)
	if !ok {
		return toolOutput
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return toolOutput
	}
	return string(b)
}

func isExaModel(resolved cfg.ResolvedModel) bool {
	if strings.EqualFold(resolved.Provider, "exa") {
		return true
	}
	if strings.Contains(strings.ToLower(resolved.Command), "exa") {
		return true
	}
	return false
}

func buildExaEnvelope(toolOutput string, exaCfg cfg.ExaConfig) (exaEnvelope, bool) {
	var resp exaSearchResponse
	if err := json.Unmarshal([]byte(toolOutput), &resp); err != nil || len(resp.Results) == 0 {
		return exaEnvelope{}, false
	}
	cfgWithDefaults := normalizeExaConfig(exaCfg)
	env := exaEnvelope{
		Provider:         "exa",
		Mode:             cfgWithDefaults.Mode,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		FullTextIncluded: false,
		Results:          make([]exaEnvelopeResult, 0, len(resp.Results)),
	}
	for _, result := range resp.Results {
		highlights := clipHighlights(result.Highlights, cfgWithDefaults.HighlightsMaxCharacters)
		summary := firstNonEmpty(result.Summary, result.SummaryText)
		textPtr := (*string)(nil)
		textTruncated := false
		if cfgWithDefaults.DefaultFullText && cfgWithDefaults.AllowFullText && strings.TrimSpace(result.Text) != "" {
			text := truncateRunes(strings.TrimSpace(result.Text), cfgWithDefaults.TextMaxCharacters)
			textPtr = &text
			textTruncated = len([]rune(strings.TrimSpace(result.Text))) > len([]rune(text))
			env.FullTextIncluded = true
		}
		signals := exaEnvelopeSignals{
			HasSummary:    strings.TrimSpace(summary) != "",
			HasHighlights: len(highlights) > 0,
			HasText:       textPtr != nil,
		}
		highlightChars := 0
		for _, h := range highlights {
			highlightChars += len([]rune(h))
		}
		if (!signals.HasSummary && !signals.HasHighlights) || (!signals.HasHighlights && len([]rune(summary)) < 120) || highlightChars < cfgWithDefaults.MinHighlightChars {
			signals.LowSignal = true
			if !env.FullTextIncluded && cfgWithDefaults.AllowFullText && cfgWithDefaults.FullTextOnLowSignal && strings.TrimSpace(result.Text) != "" {
				text := truncateRunes(strings.TrimSpace(result.Text), lowSignalTextLimit(cfgWithDefaults))
				textPtr = &text
				textTruncated = len([]rune(strings.TrimSpace(result.Text))) > len([]rune(text))
				signals.HasText = true
				env.FullTextIncluded = true
				env.FallbackReason = "low_signal"
			}
		}
		env.Results = append(env.Results, exaEnvelopeResult{
			ID:            result.ID,
			Title:         result.Title,
			URL:           result.URL,
			Domain:        domainFromURL(result.URL),
			PublishedDate: result.PublishedDate,
			Author:        result.Author,
			Summary:       summary,
			Highlights:    highlights,
			Text:          textPtr,
			TextTruncated: textTruncated,
			SourceType:    sourceTypeFromURL(result.URL),
			Subpages:      mapSubpages(result.Subpages, cfgWithDefaults.HighlightsMaxCharacters),
			Signals:       signals,
		})
	}
	return env, true
}

func normalizeExaConfig(exaCfg cfg.ExaConfig) cfg.ExaConfig {
	if exaCfg.Mode == "" {
		exaCfg.Mode = "envelope"
	}
	if exaCfg.MinHighlightChars <= 0 {
		exaCfg.MinHighlightChars = 300
	}
	if exaCfg.HighlightsMaxCharacters <= 0 {
		exaCfg.HighlightsMaxCharacters = 1200
	}
	if exaCfg.TextMaxCharacters < 0 {
		exaCfg.TextMaxCharacters = 0
	}
	if exaCfg.MaxAgeHours <= 0 {
		exaCfg.MaxAgeHours = 24
	}
	if !exaCfg.AllowFullText && !exaCfg.DefaultFullText {
		exaCfg.AllowFullText = true
	}
	return exaCfg
}

func lowSignalTextLimit(exaCfg cfg.ExaConfig) int {
	if exaCfg.TextMaxCharacters > 0 {
		if exaCfg.TextMaxCharacters > 8000 {
			return 8000
		}
		if exaCfg.TextMaxCharacters < 4000 {
			return 4000
		}
		return exaCfg.TextMaxCharacters
	}
	return 4000
}

func clipHighlights(highlights []string, maxChars int) []string {
	if len(highlights) == 0 {
		return nil
	}
	if maxChars <= 0 {
		maxChars = 1200
	}
	remaining := maxChars
	out := make([]string, 0, len(highlights))
	for _, h := range highlights {
		h = strings.TrimSpace(h)
		if h == "" || remaining <= 0 {
			continue
		}
		clipped := truncateRunes(h, remaining)
		out = append(out, clipped)
		remaining -= len([]rune(clipped))
		if remaining <= 0 {
			break
		}
	}
	return out
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func domainFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func sourceTypeFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "unknown"
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.Contains(host, "arxiv.org"):
		return "paper"
	case strings.Contains(host, "github.com"):
		return "github"
	case strings.Contains(host, "news"):
		return "news"
	case strings.Contains(host, "docs.") || strings.Contains(host, "developer"):
		return "official"
	case strings.HasSuffix(strings.ToLower(path.Ext(u.Path)), ".pdf"):
		return "paper"
	default:
		return "web"
	}
}

func mapSubpages(subpages []exaSubpage, maxChars int) []exaEnvelopeSubpage {
	if len(subpages) == 0 {
		return nil
	}
	out := make([]exaEnvelopeSubpage, 0, len(subpages))
	for _, sp := range subpages {
		out = append(out, exaEnvelopeSubpage{
			Title:      sp.Title,
			URL:        sp.URL,
			Summary:    strings.TrimSpace(sp.Summary),
			Highlights: clipHighlights(sp.Highlights, maxChars/2),
		})
	}
	return out
}
