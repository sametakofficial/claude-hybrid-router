//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemCertBundlePath: macOS exposes system roots through the Security
// framework, not a filesystem bundle. Several tools still honour /etc/ssl/cert.pem
// (installed by openssl@3 / homebrew), so we point downstream env vars at the
// bundle we synthesise from that plus our CA.
func systemCertBundlePath() string {
	for _, p := range []string{
		"/etc/ssl/cert.pem",
		"/opt/homebrew/etc/openssl@3/cert.pem",
		"/usr/local/etc/openssl@3/cert.pem",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/etc/ssl/cert.pem"
}

func installTrustPlatform(certPath string) error {
	// Admin-wide install into the System keychain as a root trust anchor.
	return runElevated("security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain", certPath)
}

func systemTrustInstalledPlatform() bool {
	home, _ := os.UserHomeDir()
	keychains := []string{"/Library/Keychains/System.keychain"}
	if home != "" {
		keychains = append(keychains, filepath.Join(home, "Library", "Keychains", "login.keychain-db"))
	}
	for _, kc := range keychains {
		out, err := exec.Command("security", "find-certificate",
			"-c", "claude-hybrid MITM CA", "-a", kc).CombinedOutput()
		if err == nil && strings.Contains(string(out), "claude-hybrid MITM CA") {
			return true
		}
	}
	return false
}
