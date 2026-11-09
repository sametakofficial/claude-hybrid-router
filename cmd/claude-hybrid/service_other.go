//go:build !linux

package main

import "fmt"

const serviceName = "claude-hybrid"

func systemctl(...string) error { return fmt.Errorf("systemd is Linux only") }

func installService() error {
	return fmt.Errorf("install-service is Linux only; use 'claude-hybrid start' elsewhere")
}
