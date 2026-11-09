package stats

import (
	"testing"
)

func TestRecorderRingAndSummary(t *testing.T) {
	var seen []Entry
	r := New(3, func(e Entry) { seen = append(seen, e) })
	r.Add(Entry{Kind: "anthropic", InputTokens: 10, OutputTokens: 1, LatencyMS: 100})
	r.Add(Entry{Kind: "routed", Provider: "gpt", Model: "m", InputTokens: 5, LatencyMS: 50})
	r.Add(Entry{Kind: "routed", Provider: "gpt", Model: "m", Status: 502, LatencyMS: 150})
	r.Add(Entry{Kind: "anthropic", Error: "cut"})
	if len(seen) != 4 || seen[0].ID != 1 || seen[3].ID != 4 || seen[0].Time.IsZero() {
		t.Fatalf("onAdd: %+v", seen)
	}
	all := r.Since(0)
	if len(all) != 3 || all[0].ID != 2 {
		t.Fatalf("ring should keep the last 3: %+v", all)
	}
	if got := r.Since(3); len(got) != 1 || got[0].ID != 4 {
		t.Fatalf("since: %+v", got)
	}
	s := r.Summary()
	if s.Requests != 4 || s.Routed != 2 || s.Errors != 2 || len(s.Targets) != 2 {
		t.Fatalf("summary: %+v", s)
	}
	gpt := s.Targets[1] // tie on requests: sorted by name
	if gpt.Target != "gpt/m" || gpt.Requests != 2 || gpt.Errors != 1 || gpt.AvgLatencyMS != 100 || gpt.InputTokens != 5 {
		t.Fatalf("target: %+v", gpt)
	}
}
