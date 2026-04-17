//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

// systemCertBundlePath: macOS exposes system roots through the Security
// framework, not a filesystem bundle. Several tools still honour /etc/ssl/cert.pem
// (installed by openssl@3 / homebrew), so we point downstream env vars at the
// bundle we synthesise from that plus our CA.
func systemCertBundlePath() string { return "/etc/ssl/cert.pem" }

func installTrustPlatform(certPath string) error {
	// Admin-wide install into the System keychain as a root trust anchor.
	return runElevated("security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain", certPath)
}

func systemTrustInstalledPlatform() bool {
	out, err := exec.Command("security", "find-certificate",
		"-c", "claude-hybrid MITM CA", "-a",
		"/Library/Keychains/System.keychain").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "claude-hybrid MITM CA")
}
