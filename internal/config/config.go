// Package config provides constants and configuration for the proxy.
package config

import "time"

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
