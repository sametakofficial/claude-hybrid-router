package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Home returns the claude-hybrid state directory (~/.claude-hybrid, or $CLAUDE_HYBRID_HOME).
func Home() string {
	if h := os.Getenv("CLAUDE_HYBRID_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude-hybrid"
	}
	return filepath.Join(home, ".claude-hybrid")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Path is the config file (~/.claude-hybrid/config.yaml).
func Path() string { return filepath.Join(Home(), "config.yaml") }

// TrashDir is where removed agent files are moved.
func TrashDir() string { return filepath.Join(Home(), "trash") }
