package provider

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

func TestRegistryMatchesConfigTypes(t *testing.T) {
	var names []string
	for _, s := range Registry {
		names = append(names, s.Name)
		if s.Label == "" || s.Help == "" {
			t.Errorf("%s: label and help are shown in the panel", s.Name)
		}
	}
	if !slices.Equal(names, config.ProviderTypes) {
		t.Fatalf("registry %v != config.ProviderTypes %v", names, config.ProviderTypes)
	}
}

func get(url string) func() (*http.Request, error) {
	return func() (*http.Request, error) { return http.NewRequest(http.MethodPost, url, nil) }
}

func TestClientDoesNotRetryTimeouts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	c := NewClient(NewTransport(50 * time.Millisecond))
	c.Backoff = time.Millisecond
	start := time.Now()
	if _, err := c.Do(context.Background(), get(srv.URL)); err == nil {
		t.Fatal("want a timeout")
	}
	// Before: a header timeout was retried twice (3 POSTs, 3x the wait).
	if hits.Load() != 1 || time.Since(start) > 180*time.Millisecond {
		t.Fatalf("timeout retried: %d hits in %v", hits.Load(), time.Since(start))
	}
}

func TestClientRetriesRefusedAnd503(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close() // nothing listens: connection refused
	c := NewClient(NewTransport(time.Second))
	c.Backoff = time.Millisecond
	var built int
	_, err := c.Do(context.Background(), func() (*http.Request, error) { built++; return http.NewRequest("POST", dead, nil) })
	if err == nil || built != 3 {
		t.Fatalf("refused should be retried: built=%d err=%v", built, err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	resp, err := c.Do(context.Background(), get(srv.URL))
	if err != nil || resp.StatusCode != 200 || hits.Load() != 3 {
		t.Fatalf("503 retry: %v %d", err, hits.Load())
	}
	hits.Store(0)
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer srv500.Close()
	if resp, _ := c.Do(context.Background(), get(srv500.URL)); resp.StatusCode != 500 || hits.Load() != 1 {
		t.Fatalf("500 must not be retried: %d", hits.Load())
	}
}

func TestRedact(t *testing.T) {
	if got := Redact(`bad key sk-abcdefghijkl and Bearer xyzxyzxyzxyz`); got != "bad key sk-[REDACTED] and Bearer [REDACTED]" {
		t.Fatal(got)
	}
}
