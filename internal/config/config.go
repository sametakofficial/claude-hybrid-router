// Package config provides constants and configuration for the proxy.
package config

import (
	"os"
	"strconv"
	"time"
)

const (
	UpstreamTimeout    = 30 * time.Second
	MaxBodyBytes       = 10 << 20 // 10 MB
	ClientRecvTimeout  = 5 * time.Minute
	MaxProxyGoroutines = 128

	MitmCacheMaxSize      = 256
	MitmCertValidityHours = 72.0

	EgressMaxRetries   = 2             // additional attempts after first failure
	EgressRetryBaseMs  = 1000          // first retry delay in ms (doubles each retry)
	EgressRetryJitter  = 500           // random jitter added to delay in ms
)

// CommandBridgeTimeoutMS is the `timeout` value injected into every Bash
// tool_use emitted by the command bridge (opencode runs, custom shell
// bridges). Claude Code's Bash tool otherwise falls back to its own
// default — observed to cut off long-running agent tasks at ~10 minutes.
// Override via COMMAND_BRIDGE_TIMEOUT_MS. Max accepted by CC's Bash tool
// is 7,200,000 (2h); higher values are clamped silently on the CC side.
var CommandBridgeTimeoutMS = envInt("COMMAND_BRIDGE_TIMEOUT_MS", 1_800_000)

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
