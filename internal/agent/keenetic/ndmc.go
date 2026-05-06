// Package keenetic isolates Keenetic OS-specific integrations: ndmc binary
// invocation, running-config parsing, and NDMS-name → Linux-iface mapping.
package keenetic

import (
	"context"
	"fmt"
)

// CmdRunner mirrors checks.Runner — redeclared here to keep keenetic a leaf
// package without an import cycle on `checks`.
type CmdRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// NDMC is a thin wrapper around the /bin/ndmc binary on KeeneticOS. The binary
// is set-uid root and talks to the local ndm core process via a unix socket,
// so it requires no auth credentials when run as root (which the agent is, via
// Entware init.d).
type NDMC struct {
	Runner CmdRunner
	// BinaryPath defaults to "/bin/ndmc" if empty.
	BinaryPath string
}

func (n NDMC) bin() string {
	if n.BinaryPath != "" {
		return n.BinaryPath
	}
	return "/bin/ndmc"
}

// Show runs `ndmc -c "show <subcmd>"`. Returns raw stdout.
func (n NDMC) Show(ctx context.Context, subcmd string) (string, error) {
	out, err := n.Runner.Run(ctx, n.bin(), "-c", "show "+subcmd)
	if err != nil {
		return "", fmt.Errorf("ndmc show %s: %w", subcmd, err)
	}
	return out, nil
}
