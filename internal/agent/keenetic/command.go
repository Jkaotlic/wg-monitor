package keenetic

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// CommandContext starts firmware ndmc with the firmware's dynamic libraries.
// Entware init scripts can export a library path meant for /opt binaries.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if filepath.Base(name) == "ndmc" {
		cmd.Env = slices.DeleteFunc(cmd.Environ(), func(v string) bool {
			return strings.HasPrefix(v, "LD_LIBRARY_PATH=") || strings.HasPrefix(v, "LD_PRELOAD=")
		})
	}
	return cmd
}
