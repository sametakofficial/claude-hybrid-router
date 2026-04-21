//go:build linux

package proxy

import (
	"context"
	"net"
	"syscall"
)

// fwmark must match the value in iptables_linux.go. The proxy marks all
// its own outgoing connections so that the iptables DNAT rule can skip them,
// preventing an infinite redirect loop.
const fwmark = 0x1f2e3d

// markedDialer returns a DialContext function that sets SO_MARK on the
// underlying socket before connecting. This mark is inherited by the TCP
// connection and visible to iptables -m mark.
func markedDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			var setErr error
			if err := c.Control(func(fd uintptr) {
				setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, fwmark)
			}); err != nil {
				return err
			}
			return setErr
		},
	}).DialContext
}
