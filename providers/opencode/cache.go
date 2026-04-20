package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sessionCache maps agent-name → opencode sessionID, with TTL-based expiry
// and optional disk persistence for cross-process reuse (each claude-hybrid
// invocation spawns its own bridge process; the on-disk JSON lets a fresh
// process resume the same opencode session until TTL expires).
type sessionCache struct {
	mu       sync.Mutex
	entries  map[string]sessionEntry
	ttl      time.Duration
	now      func() time.Time
	diskPath string // empty → memory-only
}

type sessionEntry struct {
	ID     string    `json:"id"`
	LastAt time.Time `json:"last_at"`
}

func newSessionCache(diskPath string, ttl time.Duration) *sessionCache {
	c := &sessionCache{
		entries:  map[string]sessionEntry{},
		ttl:      ttl,
		now:      time.Now,
		diskPath: diskPath,
	}
	c.loadFromDisk()
	return c
}

func (c *sessionCache) loadFromDisk() {
	if c.diskPath == "" {
		return
	}
	data, err := os.ReadFile(c.diskPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("session cache: read %s: %v", c.diskPath, err)
		}
		return
	}
	var persisted map[string]sessionEntry
	if err := json.Unmarshal(data, &persisted); err != nil {
		log.Printf("session cache: parse %s: %v", c.diskPath, err)
		return
	}
	now := c.now()
	kept := 0
	for agent, e := range persisted {
		if now.Sub(e.LastAt) > c.ttl {
			continue
		}
		c.entries[agent] = e
		kept++
	}
	if kept > 0 {
		log.Printf("session cache: loaded %d entries from %s", kept, c.diskPath)
	}
}

// saveToDisk writes the current cache atomically. Called with c.mu held.
func (c *sessionCache) saveToDisk() {
	if c.diskPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.diskPath), 0o755); err != nil {
		log.Printf("session cache: mkdir: %v", err)
		return
	}
	data, err := json.MarshalIndent(c.entries, "", "  ")
	if err != nil {
		return
	}
	tmp := c.diskPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("session cache: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, c.diskPath); err != nil {
		log.Printf("session cache: rename %s: %v", c.diskPath, err)
	}
}

// Get returns the cached sessionID for agent or "". A hit refreshes the
// entry's LastAt so active agents stay alive.
func (c *sessionCache) Get(agent string) string {
	if agent == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[agent]
	if !ok {
		return ""
	}
	if c.now().Sub(e.LastAt) > c.ttl {
		delete(c.entries, agent)
		c.saveToDisk()
		return ""
	}
	e.LastAt = c.now()
	c.entries[agent] = e
	c.saveToDisk()
	return e.ID
}

// Set stores sessionID for agent (overwriting any prior entry).
func (c *sessionCache) Set(agent, sessionID string) {
	if agent == "" || sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[agent] = sessionEntry{ID: sessionID, LastAt: c.now()}
	c.saveToDisk()
}

// Forget removes the cache entry for agent.
func (c *sessionCache) Forget(agent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, agent)
	c.saveToDisk()
}
