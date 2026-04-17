package main

import (
	"fmt"
	"os"
)

func runTrust(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: claude-hybrid trust <install|status>")
		return 2
	}
	certsDir := defaultCertsDir()
	if err := os.MkdirAll(certsDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "create cert dir: %v\n", err)
		return 1
	}
	certPath, _, _, _, err := ensureCA(certsDir, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ensure CA: %v\n", err)
		return 1
	}

	switch args[0] {
	case "install":
		if err := installTrustPlatform(certPath); err != nil {
			fmt.Fprintf(os.Stderr, "install trust: %v\n", err)
			return 1
		}
		fmt.Printf("Installed %s into system trust store.\n", certPath)
		fmt.Printf("Downstream tools should set SSL_CERT_FILE=%s for bundle-only compatibility.\n", systemCertBundlePath())
		return 0
	case "status":
		installed := systemTrustInstalledPlatform()
		fmt.Printf("CA path: %s\n", certPath)
		fmt.Printf("System trust: %v\n", installed)
		if installed {
			return 0
		}
		return 1
	default:
		fmt.Fprintf(os.Stderr, "unknown trust subcommand %q\n", args[0])
		return 2
	}
}
