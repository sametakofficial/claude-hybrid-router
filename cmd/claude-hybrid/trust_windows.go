//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemCertBundlePath: Windows has no canonical PEM bundle. Tools that
// expect a file (curl, Python requests, git) need SSL_CERT_FILE pointed at
// our generated combined bundle. The launcher overrides that env var.
func systemCertBundlePath() string {
	if v := os.Getenv("SSL_CERT_FILE"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude-hybrid", "certs", "bundle.crt")
}

func installTrustPlatform(certPath string) error {
	// Install into the *current user's* ROOT store — avoids UAC elevation
	// and covers Node/Python/curl spawned by the same user.
	cmd := exec.Command("certutil", "-user", "-addstore", "-f", "ROOT", certPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func systemTrustInstalledPlatform() bool {
	out, err := exec.Command("certutil", "-user", "-store", "ROOT").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "claude-hybrid mitm ca")
}
