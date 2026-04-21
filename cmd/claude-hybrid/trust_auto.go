package main

import "log"

func ensureSystemTrustInstalled(certPath string) (bool, error) {
	if systemTrustInstalledPlatform() {
		return true, nil
	}
	if err := installTrustPlatform(certPath); err != nil {
		log.Printf("warn: auto trust install failed: %v", err)
		return false, err
	}
	log.Printf("system trust installed for %s", certPath)
	return true, nil
}
