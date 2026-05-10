//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

// pidIsLive on Windows: OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION.
// If the PID exists (any state — running, suspended, even zombie before
// reaping) the open succeeds. ERROR_INVALID_PARAMETER means the PID is not
// allocated; ACCESS_DENIED means it exists but we can't open it (still
// alive).
func pidIsLive(pid int) bool {
	const access = windows.PROCESS_QUERY_LIMITED_INFORMATION
	h, err := windows.OpenProcess(access, false, uint32(pid))
	if err == nil {
		_ = windows.CloseHandle(h)
		return true
	}
	if err == windows.ERROR_ACCESS_DENIED {
		return true
	}
	return false
}
