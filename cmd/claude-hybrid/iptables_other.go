//go:build !linux

package main

// addIptablesRule is a no-op on non-Linux platforms.
func addIptablesRule(interceptPort string, logf func(string, ...interface{})) error {
	logf("iptables DNAT not available on this platform — CONNECT proxy mode only")
	return nil
}

// removeIptablesRule is a no-op on non-Linux platforms.
func removeIptablesRule(interceptPort string, logf func(string, ...interface{})) {}
