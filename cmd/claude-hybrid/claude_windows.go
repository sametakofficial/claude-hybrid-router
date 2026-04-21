//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func resolveClaudeBinary() string {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "claude"
	}
	lower := strings.ToLower(path)
	if strings.Contains(lower, "claude-desktop") || strings.Contains(lower, "claude desktop") {
		for _, dir := range []string{
			filepath.Join(os.Getenv("APPDATA"), "npm"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "npm"),
			filepath.Join(os.Getenv("USERPROFILE"), ".local", "bin"),
		} {
			for _, name := range []string{"claude.cmd", "claude.exe"} {
				candidate := filepath.Join(dir, name)
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}
	return path
}
