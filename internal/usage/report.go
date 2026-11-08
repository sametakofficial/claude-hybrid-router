package usage

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Side is the usage of one side (Anthropic subscription or routed) in a period.
type Side struct {
	Requests int64   `json:"requests"`
	Tokens   Tokens  `json:"tokens"`
	CostUSD  float64 `json:"cost_usd"` // API-equivalent value at Claude prices
}

// Period compares the two sides over a time span.
type Period struct {
	Name      string  `json:"name"`
	Anthropic Side    `json:"anthropic"`
	Routed    Side    `json:"routed"`
	Offload   float64 `json:"offload"` // routed share of the API-equivalent value, 0..1
}

// WindowReport projects one subscription rate-limit window.
type WindowReport struct {
	Name        string    `json:"name"` // "5h", "7d", ...
	Label       string    `json:"label"`
	Utilization float64   `json:"utilization"` // now, 0..1, from Anthropic
	ResetAt     int64     `json:"reset_at"`    // unix seconds
	Start       time.Time `json:"start"`       // window start used for the sums
	SeenAt      time.Time `json:"seen_at"`     // when Anthropic last reported it
	Period      Period    `json:"period"`      // usage inside this window
	Without     float64   `json:"without"`     // estimated utilization with no routing
	Saved       float64   `json:"saved"`       // Without - Utilization
	Calibrated  bool      `json:"calibrated"`  // a rate has been measured
	Calibration float64   `json:"calibration"` // 0..1 progress while not calibrated
	Rate        float64   `json:"rate"`        // utilization per USD of subscription value
	Note        string    `json:"note,omitempty"`
}

// SourceRow is routed usage per agent or rule.
type SourceRow struct {
	Source   string  `json:"source"`
	Target   string  `json:"target"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	CostUSD  float64 `json:"cost_usd"`
}

// Day is one calendar day (local time).
type Day struct {
	Date         string  `json:"date"`
	AnthropicUSD float64 `json:"anthropic_usd"`
	RoutedUSD    float64 `json:"routed_usd"`
	AnthropicTok int64   `json:"anthropic_tokens"`
	RoutedTok    int64   `json:"routed_tokens"`
}

// Report is everything the dashboard shows.
type Report struct {
	GeneratedAt  time.Time      `json:"generated_at"`
	Plan         *Plan          `json:"plan"`
	Plans        []Plan         `json:"plans"`
	Limits       Limits         `json:"limits"`
	Windows      []WindowReport `json:"windows"`
	Periods      []Period       `json:"periods"` // today, 7 days, 30 days
	Sources      []SourceRow    `json:"sources"` // routed, last 30 days
	Days         []Day          `json:"days"`    // last 30 days, oldest first
	CacheHitRate float64        `json:"cache_hit_rate"`
	// Value of 30 days of routed tokens relative to the monthly plan price.
	PlanValueRatio float64  `json:"plan_value_ratio"`
	Assumptions    []string `json:"assumptions"`
}

// cacheShape is how Claude Code's input usually splits on the subscription.
type cacheShape struct{ read, write float64 }

func observedCache(bs []bucket, since int64) (cacheShape, bool) {
	var t Tokens
	for _, b := range bs {
		if b.Kind == "anthropic" && b.Hour >= since {
			t.add(b.Tokens)
		}
	}
	all := t.AllInput()
	if all < 10000 {
		return cacheShape{}, false
	}
	return cacheShape{read: float64(t.CacheRead) / float64(all), write: float64(t.CacheWrite) / float64(all)}, true
}

// claudeEquivalent re-shapes routed tokens as if Anthropic had served them:
// providers often report no cache, but on the subscription most of the same
// input would have been a cheap cache read.
func claudeEquivalent(t Tokens, shape cacheShape, haveShape bool) Tokens {
	if !haveShape || t.CacheRead+t.CacheWrite > 0 {
		return t
	}
	all := float64(t.Input)
	read := int64(all * shape.read)
	write := int64(all * shape.write)
	return Tokens{Input: t.Input - read - write, Output: t.Output, CacheRead: read, CacheWrite: write}
}

func (b bucket) cost(shape cacheShape, haveShape bool) float64 {
	p, _ := PriceFor(b.ReqModel)
	t := b.Tokens
	if b.Kind == "routed" {
		t = claudeEquivalent(t, shape, haveShape)
	}
	return t.Cost(p)
}

func sumPeriod(name string, bs []bucket, since int64, shape cacheShape, haveShape bool) Period {
	p := Period{Name: name}
	for _, b := range bs {
		if b.Hour < since {
			continue
		}
		side := &p.Anthropic
		if b.Kind == "routed" {
			side = &p.Routed
		}
		side.Requests += b.Requests
		side.Tokens.add(b.Tokens)
		side.CostUSD += b.cost(shape, haveShape)
	}
	if total := p.Anthropic.CostUSD + p.Routed.CostUSD; total > 0 {
		p.Offload = p.Routed.CostUSD / total
	}
	return p
}

// windowLength reads names like "5h", "7d" or "7d_sonnet".
func windowLength(name string) (time.Duration, string) {
	n := strings.ToLower(name)
	i := 0
	for i < len(n) && n[i] >= '0' && n[i] <= '9' {
		i++
	}
	v, _ := strconv.Atoi(n[:i])
	if v == 0 || i == len(n) {
		return 0, name
	}
	unit, rest := n[i], n[i+1:]
	switch unit {
	case 'h':
		return time.Duration(v) * time.Hour, strconv.Itoa(v) + "-hour limit" + suffix(rest)
	case 'd':
		return time.Duration(v) * 24 * time.Hour, strconv.Itoa(v) + "-day limit" + suffix(rest)
	}
	return 0, name
}

func suffix(rest string) string {
	rest = strings.Trim(rest, "_-")
	if rest == "" {
		return ""
	}
	return " (" + rest + ")"
}

// Build creates the report. plan may be nil.
func (s *Store) Build(now time.Time, plan *Plan) Report {
	bs, limits := s.snapshot()
	r := Report{GeneratedAt: now, Plan: plan, Plans: Plans, Limits: limits}
	hourNow := now.Unix() / 3600
	day := int64(24)

	shape, haveShape := observedCache(bs, hourNow-7*day)
	if haveShape {
		r.CacheHitRate = shape.read
	}

	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	r.Periods = []Period{
		sumPeriod("today", bs, midnight.Unix()/3600, shape, haveShape),
		sumPeriod("7d", bs, hourNow-7*day+1, shape, haveShape),
		sumPeriod("30d", bs, hourNow-30*day+1, shape, haveShape),
	}

	names := make([]string, 0, len(limits.Windows))
	for n := range limits.Windows {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		a, _ := windowLength(names[i])
		b, _ := windowLength(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		w := limits.Windows[name]
		length, label := windowLength(name)
		if length == 0 {
			continue
		}
		start := now.Add(-length)
		if w.ResetAt > 0 {
			if reset := time.Unix(w.ResetAt, 0); reset.After(now) {
				start = reset.Add(-length)
			}
		}
		wr := WindowReport{Name: name, Label: label, Utilization: w.Utilization, ResetAt: w.ResetAt, Start: start, SeenAt: w.SeenAt}
		wr.Period = sumPeriod(name, bs, start.Unix()/3600, shape, haveShape)
		rate, ok, progress := s.calibration(name, w.ResetAt)
		switch {
		case w.ResetAt > 0 && time.Unix(w.ResetAt, 0).Before(now):
			wr.Note = "This window has reset since Anthropic last reported it."
		case !ok:
			wr.Calibration = progress
			wr.Note = "Calibrating: claude-hybrid learns how fast this limit fills by watching it rise while you use Claude Code."
		default:
			// Routed value converted at the measured rate. Only routed work
			// inside this window counts.
			wr.Calibrated, wr.Calibration, wr.Rate = true, 1, rate
			wr.Saved = wr.Period.Routed.CostUSD * rate
			wr.Without = w.Utilization + wr.Saved
		}
		r.Windows = append(r.Windows, wr)
	}

	// Routed usage by agent/rule over 30 days.
	since30 := hourNow - 30*day + 1
	rows := map[string]*SourceRow{}
	for _, b := range bs {
		if b.Kind != "routed" || b.Hour < since30 {
			continue
		}
		src := b.Source
		if src == "" {
			src = "(unknown)"
		}
		target := b.Provider + "/" + b.Model
		k := src + "|" + target
		row := rows[k]
		if row == nil {
			row = &SourceRow{Source: src, Target: target}
			rows[k] = row
		}
		row.Requests += b.Requests
		row.Tokens += b.Tokens.Total()
		row.CostUSD += b.cost(shape, haveShape)
	}
	for _, row := range rows {
		r.Sources = append(r.Sources, *row)
	}
	sort.Slice(r.Sources, func(i, j int) bool { return r.Sources[i].CostUSD > r.Sources[j].CostUSD })

	// Daily series.
	days := map[string]*Day{}
	for i := 29; i >= 0; i-- {
		d := midnight.AddDate(0, 0, -i).Format("2006-01-02")
		days[d] = &Day{Date: d}
		r.Days = append(r.Days, Day{Date: d})
	}
	for _, b := range bs {
		d := time.Unix(b.Hour*3600, 0).In(now.Location()).Format("2006-01-02")
		dd := days[d]
		if dd == nil {
			continue
		}
		c := b.cost(shape, haveShape)
		if b.Kind == "routed" {
			dd.RoutedUSD += c
			dd.RoutedTok += b.Tokens.Total()
		} else {
			dd.AnthropicUSD += c
			dd.AnthropicTok += b.Tokens.Total()
		}
	}
	for i := range r.Days {
		r.Days[i] = *days[r.Days[i].Date]
	}

	if plan != nil && plan.PriceUSD > 0 {
		r.PlanValueRatio = r.Periods[2].Routed.CostUSD / plan.PriceUSD
	}
	r.Assumptions = []string{
		"Token values use Anthropic API list prices of the Claude model Claude Code asked for.",
		"Routed input is counted with the cache-hit rate seen on your subscription traffic, since providers often report no cache.",
		"The limit rate is measured: how many points a window rose per dollar of subscription traffic seen here.",
		"Usage from claude.ai or other computers also raises the limit, which makes the measured rate (and the savings) look higher.",
	}
	return r
}
