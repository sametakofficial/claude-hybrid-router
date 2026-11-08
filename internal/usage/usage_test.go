package usage

import (
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestPriceFor(t *testing.T) {
	cases := map[string]float64{
		"claude-opus-5-5":            4,
		"claude-opus-4-5-20251101":   5,
		"claude-opus-4-1":            15,
		"claude-haiku-5-5":           0.10,
		"claude-sonnet-4-6":          3,
		"claude-3-5-sonnet-20241022": 3,
	}
	for m, want := range cases {
		if p, ok := PriceFor(m); !ok || p.Input != want {
			t.Errorf("%s: got %v %v, want %v", m, p.Input, ok, want)
		}
	}
	if _, ok := PriceFor("claude-test"); ok {
		t.Error("unknown model should report not known")
	}
}

func TestObserveHeaders(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "u.json"))
	defer s.Close()
	reset := time.Now().Add(48 * time.Hour).Unix()
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.67")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(reset, 10))
	h.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day")
	h.Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
	h.Set("Anthropic-Ratelimit-Unified-Fallback-Percentage", "0.5")
	s.ObserveHeaders(h)
	s.ObserveHeaders(http.Header{"Content-Type": {"x"}}) // ignored
	_, l := s.snapshot()
	w, ok := l.Windows["7d"]
	if !ok || w.Utilization != 0.67 || w.ResetAt != reset || l.Status != "allowed" || l.Claim != "seven_day" {
		t.Fatalf("limits %+v", l)
	}
	if len(l.Windows) != 1 {
		t.Fatalf("overage/fallback must not become windows: %+v", l.Windows)
	}
}

func limitHeader(util string, reset int64) http.Header {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", util)
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(reset, 10))
	return h
}

func TestReportEstimatesSavings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.json")
	s, _ := Open(path)
	now := time.Now()
	reset := now.Add(24 * time.Hour).Unix()

	// Before any traffic the limit is already 60% used (from earlier work
	// we never saw). That must not inflate the estimate.
	s.ObserveHeaders(limitHeader("0.60", reset))
	if w := s.Build(now, nil).Windows[0]; w.Calibrated {
		t.Fatal("one reading cannot calibrate")
	}
	// $4 of subscription traffic (1M uncached input on Opus 5.5) raises it 4 points.
	s.Add(Record{Time: now.Add(-time.Hour), Kind: "anthropic", ReqModel: "claude-opus-5-5", Tokens: Tokens{Input: 1_000_000}})
	s.ObserveHeaders(limitHeader("0.64", reset))
	// Routed: 1M output that would have been Opus 5.5 = $20.
	s.Add(Record{Time: now.Add(-time.Hour), Kind: "routed", Source: "agent:coder", Provider: "letum", Model: "letum", ReqModel: "claude-opus-5-5", Tokens: Tokens{Output: 1_000_000}})

	plan, _ := PlanByID("max5", 0)
	r := s.Build(now, &plan)
	if len(r.Windows) != 1 || !r.Windows[0].Calibrated {
		t.Fatalf("windows %+v", r.Windows)
	}
	w := r.Windows[0]
	// 4 points per $4 = 1 point per $1, so $20 routed saved 20 points.
	if !near(w.Rate, 0.01) || !near(w.Saved, 0.20) || !near(w.Without, 0.84) {
		t.Fatalf("rate=%v saved=%v without=%v", w.Rate, w.Saved, w.Without)
	}
	if p := r.Periods[1]; !near(p.Offload, 20.0/24.0) {
		t.Fatalf("offload %v", p.Offload)
	}
	if len(r.Sources) != 1 || r.Sources[0].Source != "agent:coder" || !near(r.Sources[0].CostUSD, 20) {
		t.Fatalf("sources %+v", r.Sources)
	}
	if !near(r.PlanValueRatio, 0.2) {
		t.Fatalf("plan value %v", r.PlanValueRatio)
	}

	// Rate and totals persist across restarts.
	s.Close()
	s2, _ := Open(path)
	defer s2.Close()
	r2 := s2.Build(now, nil)
	if !near(r2.Periods[1].Routed.CostUSD, 20) || len(r2.Windows) != 1 || !near(r2.Windows[0].Rate, 0.01) {
		t.Fatalf("not persisted: %+v %+v", r2.Periods[1], r2.Windows)
	}
}

func TestSmallRiseIsStillCalibrating(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "u.json"))
	defer s.Close()
	reset := time.Now().Add(time.Hour).Unix()
	s.ObserveHeaders(limitHeader("0.10", reset))
	s.Add(Record{Kind: "anthropic", ReqModel: "claude-opus-5-5", Tokens: Tokens{Input: 100_000}})
	s.ObserveHeaders(limitHeader("0.11", reset))
	w := s.Build(time.Now(), nil).Windows[0]
	if w.Calibrated || w.Saved != 0 || !near(w.Calibration, 1.0/3) {
		t.Fatalf("%+v", w)
	}
}

func TestRoutedInputGetsSubscriptionCacheShape(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "u.json"))
	defer s.Close()
	now := time.Now()
	// Subscription: 90% of input is cache reads.
	s.Add(Record{Time: now, Kind: "anthropic", ReqModel: "claude-opus-5-5", Tokens: Tokens{Input: 10_000, CacheRead: 90_000}})
	s.Add(Record{Time: now, Kind: "routed", ReqModel: "claude-opus-5-5", Tokens: Tokens{Input: 1_000_000}})
	r := s.Build(now, nil)
	// 100k uncached at $4 + 900k cached at $0.20 = $0.40 + $0.18
	if got := r.Periods[0].Routed.CostUSD; !near(got, 0.58) {
		t.Fatalf("routed cost %v", got)
	}
	if !near(r.CacheHitRate, 0.9) {
		t.Fatalf("cache hit rate %v", r.CacheHitRate)
	}
}
