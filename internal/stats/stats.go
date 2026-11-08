// Package stats keeps a ring buffer of recent requests and running totals.
package stats

import (
	"sort"
	"sync"
	"time"
)

// Entry is one proxied API request.
type Entry struct {
	ID           int64     `json:"id"`
	Time         time.Time `json:"time"`
	Kind         string    `json:"kind"`   // "routed" or "anthropic"
	Source       string    `json:"source"` // "agent:NAME", "rule:N", "legacy" or ""
	ReqModel     string    `json:"req_model"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	Path         string    `json:"path"`
	Stream       bool      `json:"stream"`
	Status       int       `json:"status"`
	LatencyMS    int64     `json:"latency_ms"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	// Parts of InputTokens that were cache reads / cache writes.
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	Error            string `json:"error"`
}

// Target holds totals for one provider/model pair.
type Target struct {
	Target       string `json:"target"`
	Requests     int    `json:"requests"`
	Errors       int    `json:"errors"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	AvgLatencyMS int64  `json:"avg_latency_ms"`
	totalLatency int64
}

// Summary is the totals since start.
type Summary struct {
	Requests int      `json:"requests"`
	Routed   int      `json:"routed"`
	Errors   int      `json:"errors"`
	Targets  []Target `json:"targets"`
}

// Recorder is safe for concurrent use.
type Recorder struct {
	mu      sync.Mutex
	size    int
	entries []Entry
	nextID  int64
	sum     Summary
	targets map[string]*Target
	onAdd   func(Entry)
}

// New creates a recorder keeping the last size entries. onAdd (optional)
// is called for every entry, e.g. to write a log line.
func New(size int, onAdd func(Entry)) *Recorder {
	return &Recorder{size: size, nextID: 1, targets: map[string]*Target{}, onAdd: onAdd}
}

// Add records a finished request.
func (r *Recorder) Add(e Entry) {
	r.mu.Lock()
	e.ID = r.nextID
	r.nextID++
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	r.entries = append(r.entries, e)
	if len(r.entries) > r.size {
		r.entries = r.entries[len(r.entries)-r.size:]
	}
	failed := e.Status >= 400 || e.Error != ""
	r.sum.Requests++
	if e.Kind == "routed" {
		r.sum.Routed++
	}
	if failed {
		r.sum.Errors++
	}
	name := "anthropic"
	if e.Provider != "" {
		name = e.Provider
	}
	if e.Model != "" {
		name += "/" + e.Model
	}
	t := r.targets[name]
	if t == nil {
		t = &Target{Target: name}
		r.targets[name] = t
	}
	t.Requests++
	if failed {
		t.Errors++
	}
	t.InputTokens += e.InputTokens
	t.OutputTokens += e.OutputTokens
	t.totalLatency += e.LatencyMS
	t.AvgLatencyMS = t.totalLatency / int64(t.Requests)
	onAdd := r.onAdd
	r.mu.Unlock()
	if onAdd != nil {
		onAdd(e)
	}
}

// Since returns entries with an id greater than id, oldest first.
func (r *Recorder) Since(id int64) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Entry{}
	for _, e := range r.entries {
		if e.ID > id {
			out = append(out, e)
		}
	}
	return out
}

// Summary returns the totals, busiest targets first.
func (r *Recorder) Summary() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sum
	s.Targets = make([]Target, 0, len(r.targets))
	for _, t := range r.targets {
		s.Targets = append(s.Targets, *t)
	}
	sort.Slice(s.Targets, func(i, j int) bool { return s.Targets[i].Requests > s.Targets[j].Requests })
	return s
}
