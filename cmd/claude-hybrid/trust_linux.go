//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// systemCertBundlePath returns the first existing well-known system CA bundle.
// Covers Debian/Ubuntu, Fedora/RHEL, OpenSUSE, Arch, Alpine — each distro
// installs the combined PEM at a different path.
func systemCertBundlePath() string {
	for _, p := range []string{
		"/etc/ssl/certs/ca-certificates.crt",                // Debian/Ubuntu/Arch/Alpine
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // Fedora/RHEL
		"/etc/pki/tls/certs/ca-bundle.crt",                  // CentOS
		"/etc/ssl/ca-bundle.pem",                            // OpenSUSE
		"/etc/ssl/cert.pem",                                 // fallback
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/etc/ssl/certs/ca-certificates.crt"
}

type distroFamily int

const (
	distroUnknown distroFamily = iota
	distroDebian
	distroFedora
	distroArch
	distroAlpine
)

func detectDistro() distroFamily {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return distroUnknown
	}
	lower := strings.ToLower(string(data))
	switch {
	case contains(lower, "id=debian"), contains(lower, "id=ubuntu"),
		contains(lower, "id=linuxmint"), contains(lower, "id=pop"),
		contains(lower, "id=elementary"), contains(lower, "id_like=debian"),
		contains(lower, "id_like=\"debian"):
		return distroDebian
	case contains(lower, "id=fedora"), contains(lower, "id=rhel"),
		contains(lower, "id=centos"), contains(lower, "id=rocky"),
		contains(lower, "id=almalinux"), contains(lower, "id_like=rhel"),
		contains(lower, "id_like=\"rhel"), contains(lower, "id_like=fedora"),
		contains(lower, "id_like=\"fedora"):
		return distroFedora
	case contains(lower, "id=arch"), contains(lower, "id=manjaro"),
		contains(lower, "id=endeavouros"), contains(lower, "id_like=arch"):
		return distroArch
	case contains(lower, "id=alpine"):
		return distroAlpine
	}
	return distroUnknown
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func installTrustPlatform(certPath string) error {
	d := detectDistro()
	switch d {
	case distroDebian, distroAlpine:
		// Copy to /usr/local/share/ca-certificates/ with .crt extension, then refresh bundle.
		target := "/usr/local/share/ca-certificates/claude-hybrid.crt"
		if err := runElevated("install", "-m", "0644", certPath, target); err != nil {
			return fmt.Errorf("copy cert: %w", err)
		}
		return runElevated("update-ca-certificates")
	case distroFedora, distroArch:
		if err := runElevated("trust", "anchor", certPath); err != nil {
			return fmt.Errorf("trust anchor: %w", err)
		}
		// Arch relies solely on `trust` (p11-kit) — update-ca-trust may be absent.
		if _, err := exec.LookPath("update-ca-trust"); err == nil {
			_ = runElevated("update-ca-trust")
		}
		return nil
	default:
		// Probe in order of likelihood.
		if _, err := exec.LookPath("trust"); err == nil {
			if err := runElevated("trust", "anchor", certPath); err == nil {
				if _, err := exec.LookPath("update-ca-trust"); err == nil {
					_ = runElevated("update-ca-trust")
				}
				return nil
			}
		}
		if _, err := exec.LookPath("update-ca-certificates"); err == nil {
			target := "/usr/local/share/ca-certificates/claude-hybrid.crt"
			if err := runElevated("install", "-m", "0644", certPath, target); err != nil {
				return fmt.Errorf("copy cert: %w", err)
			}
			return runElevated("update-ca-certificates")
		}
		return fmt.Errorf("no supported trust tool found (tried: trust, update-ca-certificates)")
	}
}

func systemTrustInstalledPlatform() bool {
	if out, err := exec.Command("trust", "list").CombinedOutput(); err == nil {
		if strings.Contains(strings.ToLower(string(out)), "claude-hybrid mitm ca") {
			return true
		}
	}
	// Fallback: inspect known bundle paths for our CN string.
	for _, p := range []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "claude-hybrid MITM CA") {
			return true
		}
	}
	return false
}
