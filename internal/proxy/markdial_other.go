//go:build !linux

package proxy

import (
	"context"
	"net"
)

// markedDialer returns nil on non-Linux platforms (no SO_MARK needed).
func markedDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil
}
