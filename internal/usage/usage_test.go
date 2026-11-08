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

func TestReportEstimatesSavings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.json")
	s, _ := Open(path)
	now := time.Now()
	// Subscription traffic: 1M uncached input on Opus 5.5 = $4.
	s.Add(Record{Time: now.Add(-time.Hour), Kind: "anthropic", ReqModel: "claude-opus-5-5", Tokens: Tokens{Input: 1_000_000}})
	// Routed: 1M output that would have been Opus 5.5 = $20.
	s.Add(Record{Time: now.Add(-time.Hour), Kind: "routed", Source: "agent:coder", Provider: "letum", Model: "letum", ReqModel: "claude-opus-5-5", Tokens: Tokens{Output: 1_000_000}})
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.10")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10))
	s.ObserveHeaders(h)

	plan, _ := PlanByID("max5", 0)
	r := s.Build(now, &plan)
	if len(r.Windows) != 1 || !r.Windows[0].Calibrated {
		t.Fatalf("windows %+v", r.Windows)
	}
	w := r.Windows[0]
	// $4 used 10%, so $20 more would have used 50% more: 60% total.
	if !near(w.Without, 0.6) || !near(w.Saved, 0.5) {
		t.Fatalf("without=%v saved=%v", w.Without, w.Saved)
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

	// Persists across restarts.
	s.Close()
	s2, _ := Open(path)
	defer s2.Close()
	if r2 := s2.Build(now, nil); !near(r2.Periods[1].Routed.CostUSD, 20) || len(r2.Windows) != 1 {
		t.Fatalf("not persisted: %+v", r2.Periods[1])
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
