//go:build !linux

package checks

import "net"

// IfaceDialer is a no-op on non-Linux. The agent only ships for linux/*, but we
// want go test ./... to compile on Windows. Tests of awg_routing pass an
// httptest.Server and never go through this dialer.
func IfaceDialer(_ string) *net.Dialer { return &net.Dialer{} }
