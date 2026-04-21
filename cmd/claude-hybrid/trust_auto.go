package main

import "log"

func ensureSystemTrustInstalled(certPath string, require bool) bool {
	if systemTrustInstalledPlatform() {
		return true
	}
	if err := installTrustPlatform(certPath); err != nil {
		if require {
			log.Fatalf("install trust: %v", err)
		}
		log.Printf("warn: auto trust install failed: %v", err)
		return false
	}
	log.Printf("system trust installed for %s", certPath)
	return true
}
