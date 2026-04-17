package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
)

const schemaVersionFile = "SCHEMA_VERSION"

func readSchemaVersion(certsDir string) int {
	b, err := os.ReadFile(filepath.Join(certsDir, schemaVersionFile))
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return v
}

func writeSchemaVersion(certsDir string) error {
	payload := []byte(strconv.Itoa(mitm.CertSchemaVersion))
	return os.WriteFile(filepath.Join(certsDir, schemaVersionFile), payload, 0644)
}

// invalidateIfStale inspects the on-disk schema version and, if it lags
// behind the current code, moves the existing certs dir into a timestamped
// bucket under .deleted/ (per the project's no-rm policy) and creates a
// fresh empty certs dir. ensureCA will then regenerate the CA on first use.
func invalidateIfStale(certsDir string, logf func(string, ...interface{})) error {
	info, err := os.Stat(certsDir)
	if err != nil {
		// New install — nothing to rotate.
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", certsDir)
	}
	// If the directory is empty, nothing to rotate — ensureCA will populate it.
	entries, err := os.ReadDir(certsDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	cur := readSchemaVersion(certsDir)
	if cur >= mitm.CertSchemaVersion {
		return nil
	}

	baseDir := filepath.Dir(certsDir)
	graveyard := filepath.Join(baseDir, ".deleted")
	if err := os.MkdirAll(graveyard, 0700); err != nil {
		return err
	}
	dest := filepath.Join(graveyard, fmt.Sprintf("certs-v%d-%d", cur, time.Now().Unix()))
	if logf != nil {
		logf("Cert schema v%d < v%d — rotating %s -> %s", cur, mitm.CertSchemaVersion, certsDir, dest)
	}
	if err := os.Rename(certsDir, dest); err != nil {
		return fmt.Errorf("rotate stale certs: %w", err)
	}
	return os.MkdirAll(certsDir, 0700)
}
