package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store keeps the live config in memory and persists every change.
//
// The file can also be edited by hand while the router runs. Store remembers
// a hash of the bytes it last read or wrote, so Watch only reloads real
// outside edits, and Update first picks up an outside edit instead of
// overwriting it with the older in-memory copy.
type Store struct {
	path string
	mu   sync.RWMutex
	cfg  *Config
	sum  [32]byte // of the file content cfg came from (or was written as)
}

// Open loads the config at path, creating a default one if it is missing.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.cfg = Default()
		if err := s.write(s.cfg); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.cfg, s.sum = c, sha256.Sum256(data)
	return s, nil
}

// Load reads the config at path without creating it: a missing file gives
// the defaults. For commands that only look (status, env).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Path returns the config file path.
func (s *Store) Path() string { return s.path }

// Get returns a deep copy of the current config (a YAML round trip, so it is
// not cheap). Use it only when the result will be modified; read with Current.
func (s *Store) Get() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

// Current returns the live config without copying. Callers must not modify
// it; Update always swaps in a new value instead of changing this one.
func (s *Store) Current() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Reload re-reads the config from disk if the file changed. An invalid file
// is reported and the live config stays as it was.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.syncLocked()
	return err
}

// syncLocked loads the file if its content differs from what we last saw.
func (s *Store) syncLocked() (changed bool, err error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // the next Update writes it again
	}
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(data)
	if sum == s.sum {
		return false, nil
	}
	c, err := Parse(data)
	if err != nil {
		return false, err
	}
	s.cfg, s.sum = c, sum
	return true, nil
}

// Update applies fn to a copy of the config, validates it, saves it and
// makes it live. If fn or validation fails nothing changes. An edit made to
// the file by hand since the last load is picked up first; if that edit is
// invalid, Update refuses rather than overwrite it.
func (s *Store) Update(fn func(c *Config) error) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.syncLocked(); err != nil {
		return nil, fmt.Errorf("%s was edited and is not valid, fix it first: %w", s.path, err)
	}
	next := s.cfg.Clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	next.setDefaults()
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if err := s.write(next); err != nil {
		return nil, err
	}
	s.cfg = next
	return next.Clone(), nil
}

// Watch reloads the config when the file is edited by hand, checking every
// interval until ctx ends. Our own writes are recognised and skipped. An
// invalid edit is reported once through logf and the old config stays live.
func (s *Store) Watch(ctx context.Context, interval time.Duration, logf func(format string, a ...any)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		changed, err := s.syncLocked()
		s.mu.Unlock()
		switch {
		case err != nil && err.Error() != lastErr:
			lastErr = err.Error()
			logf("config changed on disk but is invalid, keeping the old one: %v", err)
		case err == nil && changed:
			lastErr = ""
			logf("config reloaded from %s", s.path)
		case err == nil:
			lastErr = ""
		}
	}
}

// write saves atomically (temp file + rename) with 0600 since it holds keys.
func (s *Store) write(c *Config) error {
	data, err := Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.sum = sha256.Sum256(data)
	return nil
}
