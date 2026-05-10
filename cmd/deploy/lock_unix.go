//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// pidIsLive reports whether `pid` corresponds to a process that is currently
// alive in this OS. POSIX: signal(0) is the canonical liveness check —
// returns nil on live, ESRCH on dead, EPERM if the process exists but isn't
// ours (still alive).
func pidIsLive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
