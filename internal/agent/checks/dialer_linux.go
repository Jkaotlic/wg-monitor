//go:build linux

package checks

import (
	"net"
	"syscall"
)

// IfaceDialer returns a *net.Dialer that binds outgoing sockets to the named
// interface via SO_BINDTODEVICE. Requires CAP_NET_RAW or root (the agent runs
// under root via Entware init.d, so this is fine).
func IfaceDialer(iface string) *net.Dialer {
	return &net.Dialer{
		Control: func(_, _ string, c syscall.RawConn) error {
			var setErr error
			ctlErr := c.Control(func(fd uintptr) {
				setErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
			})
			if setErr != nil {
				return setErr
			}
			return ctlErr
		},
	}
}
