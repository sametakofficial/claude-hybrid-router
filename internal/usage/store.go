package usage

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	keepFor      = 40 * 24 * time.Hour
	saveInterval = 15 * time.Second
)

// Record is one finished request, as the proxy reports it.
type Record struct {
	Time     time.Time
	Kind     string // "anthropic" or "routed"
	Source   string // "agent:NAME", "rule:N", ...
	Provider string
	Model    string // model that served it
	ReqModel string // Claude model Claude Code asked for
	Failed   bool
	Tokens   Tokens
}

// bucket sums records per hour and per route.
type bucket struct {
	Hour     int64  `json:"hour"` // unix hours
	Kind     string `json:"kind"`
	Source   string `json:"source,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	ReqModel string `json:"req_model"`
	Requests int64  `json:"requests"`
	Errors   int64  `json:"errors,omitempty"`
	Tokens   Tokens `json:"tokens"`
}

func (b bucket) key() string {
	return strconv.FormatInt(b.Hour, 10) + "|" + b.Kind + "|" + b.Source + "|" + b.Provider + "|" + b.Model + "|" + b.ReqModel
}

// Window is one rate-limit window as reported by Anthropic (e.g. "5h", "7d").
type Window struct {
	Utilization float64   `json:"utilization"` // 0..1
	ResetAt     int64     `json:"reset_at"`    // unix seconds, 0 if unknown
	Status      string    `json:"status"`
	SeenAt      time.Time `json:"seen_at"`
}

// Limits is the latest subscription rate-limit state.
type Limits struct {
	SeenAt  time.Time         `json:"seen_at"`
	Status  string            `json:"status"`
	Claim   string            `json:"claim"` // which window is binding right now
	Windows map[string]Window `json:"windows"`
}

// sample is one utilization reading together with how much subscription
// value had been seen by then. Two samples in the same window give the rate
// "points of the limit per USD" without guessing the plan's quota.
type sample struct {
	Time    time.Time `json:"t"`
	Util    float64   `json:"u"`
	ResetAt int64     `json:"r"`
	CumUSD  float64   `json:"c"`
}

type fileFormat struct {
	Version int                 `json:"version"`
	Buckets []bucket            `json:"buckets"`
	Limits  Limits              `json:"limits"`
	CumUSD  float64             `json:"cum_usd"`
	Samples map[string][]sample `json:"samples"`
	Rates   map[string]float64  `json:"rates"` // last good rate per window
}

// Store is safe for concurrent use. It saves itself every few seconds.
type Store struct {
	path string

	mu      sync.Mutex
	buckets map[string]*bucket
	limits  Limits
	cumUSD  float64             // running API-equivalent value of subscription traffic
	samples map[string][]sample // per window
	rates   map[string]float64  // last calibrated rate per window (utilization per USD)
	dirty   bool
	stop    chan struct{}
	done    chan struct{}
}

// Open loads path (a missing file is fine) and starts the save loop.
func Open(path string) (*Store, error) {
	s := &Store{path: path, buckets: map[string]*bucket{}, samples: map[string][]sample{}, rates: map[string]float64{},
		stop: make(chan struct{}), done: make(chan struct{})}
	if data, err := os.ReadFile(path); err == nil {
		var f fileFormat
		if err := json.Unmarshal(data, &f); err == nil {
			for i := range f.Buckets {
				b := f.Buckets[i]
				s.buckets[b.key()] = &b
			}
			s.limits = f.Limits
			s.cumUSD = f.CumUSD
			if f.Samples != nil {
				s.samples = f.Samples
			}
			if f.Rates != nil {
				s.rates = f.Rates
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.limits.Windows == nil {
		s.limits.Windows = map[string]Window{}
	}
	go s.loop()
	return s, nil
}

func (s *Store) loop() {
	defer close(s.done)
	t := time.NewTicker(saveInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.Save()
		case <-s.stop:
			s.Save()
			return
		}
	}
}

// Close saves and stops the background loop.
func (s *Store) Close() {
	close(s.stop)
	<-s.done
}

// Add records one request.
func (s *Store) Add(r Record) {
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	b := bucket{Hour: r.Time.Unix() / 3600, Kind: r.Kind, Source: r.Source, Provider: r.Provider, Model: r.Model, ReqModel: r.ReqModel}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.buckets[b.key()]
	if cur == nil {
		cur = &b
		s.buckets[b.key()] = cur
	}
	cur.Requests++
	if r.Failed {
		cur.Errors++
	}
	cur.Tokens.add(r.Tokens)
	if r.Kind == "anthropic" {
		p, _ := PriceFor(r.ReqModel)
		s.cumUSD += r.Tokens.Cost(p)
	}
	s.dirty = true
}

const unifiedPrefix = "anthropic-ratelimit-unified-"

// ObserveHeaders reads Anthropic's subscription rate-limit headers, e.g.
// anthropic-ratelimit-unified-7d-utilization: 0.67.
func (s *Store) ObserveHeaders(h http.Header) {
	now := time.Now()
	found := false
	windows := map[string]Window{}
	var status, claim string
	for k, vals := range h {
		k = strings.ToLower(k)
		if !strings.HasPrefix(k, unifiedPrefix) || len(vals) == 0 {
			continue
		}
		found = true
		name, v := strings.TrimPrefix(k, unifiedPrefix), vals[0]
		switch name {
		case "status":
			status = v
			continue
		case "representative-claim":
			claim = v
			continue
		}
		for _, field := range []string{"-utilization", "-reset", "-status"} {
			if !strings.HasSuffix(name, field) {
				continue
			}
			win := strings.TrimSuffix(name, field)
			if win == "" || win == "overage" || win == "fallback" {
				break
			}
			w := windows[win]
			switch field {
			case "-utilization":
				w.Utilization, _ = strconv.ParseFloat(v, 64)
			case "-reset":
				w.ResetAt, _ = strconv.ParseInt(v, 10, 64)
			case "-status":
				w.Status = v
			}
			windows[win] = w
		}
	}
	if !found {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits.SeenAt = now
	if status != "" {
		s.limits.Status = status
	}
	if claim != "" {
		s.limits.Claim = claim
	}
	for name, w := range windows {
		old := s.limits.Windows[name]
		w.SeenAt = now
		if w.ResetAt == 0 {
			w.ResetAt = old.ResetAt
		}
		s.limits.Windows[name] = w
		s.addSample(name, sample{Time: now, Util: w.Utilization, ResetAt: w.ResetAt, CumUSD: s.cumUSD})
	}
	s.dirty = true
}

const maxSamples = 500

// addSample stores a reading; unchanged readings only move the last one.
// Caller holds s.mu.
func (s *Store) addSample(window string, x sample) {
	list := s.samples[window]
	if n := len(list); n >= 2 && list[n-1].Util == x.Util && list[n-2].Util == x.Util && list[n-1].ResetAt == x.ResetAt {
		list[n-1] = x
	} else {
		list = append(list, x)
	}
	if len(list) > maxSamples {
		list = list[len(list)-maxSamples:]
	}
	s.samples[window] = list
	if r, ok := rateFrom(list, x.ResetAt); ok {
		s.rates[window] = r
	}
}

// minRise is how much utilization must rise inside one window before the
// rate is trusted (Anthropic reports two decimals).
const minRise = 0.03

// rateFrom measures utilization gained per USD within the window that
// resets at resetAt.
func rateFrom(list []sample, resetAt int64) (float64, bool) {
	var first, last *sample
	for i := range list {
		if list[i].ResetAt != resetAt {
			continue
		}
		if first == nil {
			first = &list[i]
		}
		last = &list[i]
	}
	if first == nil || last == first {
		return 0, false
	}
	du, dc := last.Util-first.Util, last.CumUSD-first.CumUSD
	if du < minRise || dc <= 0 {
		return 0, false
	}
	return du / dc, true
}

// calibration returns the rate for a window and how far along calibration
// is (0..1) when there is no rate yet.
func (s *Store) calibration(window string, resetAt int64) (rate float64, ok bool, progress float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := rateFrom(s.samples[window], resetAt); ok {
		return r, true, 1
	}
	if r := s.rates[window]; r > 0 {
		return r, true, 1
	}
	var first, last *sample
	for i, x := range s.samples[window] {
		if x.ResetAt == resetAt {
			if first == nil {
				first = &s.samples[window][i]
			}
			last = &s.samples[window][i]
		}
	}
	if first != nil {
		progress = (last.Util - first.Util) / minRise
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
	}
	return 0, false, progress
}

// Save writes the file if anything changed (atomic, 0600).
func (s *Store) Save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	cutoff := time.Now().Add(-keepFor).Unix() / 3600
	f := fileFormat{Version: 1, Limits: s.limits, CumUSD: s.cumUSD, Samples: s.samples, Rates: s.rates}
	for k, b := range s.buckets {
		if b.Hour < cutoff {
			delete(s.buckets, k)
			continue
		}
		f.Buckets = append(f.Buckets, *b)
	}
	s.dirty = false
	s.mu.Unlock()

	sort.Slice(f.Buckets, func(i, j int) bool { return f.Buckets[i].key() < f.Buckets[j].key() })
	s.mu.Lock()
	data, err := json.Marshal(f)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// snapshot copies the data a report needs.
func (s *Store) snapshot() ([]bucket, Limits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bucket, 0, len(s.buckets))
	for _, b := range s.buckets {
		out = append(out, *b)
	}
	l := s.limits
	l.Windows = make(map[string]Window, len(s.limits.Windows))
	for k, v := range s.limits.Windows {
		l.Windows[k] = v
	}
	return out, l
}
