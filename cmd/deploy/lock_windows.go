//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// pidIsLive on Windows treats ACCESS_DENIED as "exists but not queryable".
func pidIsLive(pid int) bool {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return false
	}
	const access = windows.PROCESS_QUERY_LIMITED_INFORMATION
	pid32 := uint32(pid) // #nosec G115 -- bounded above before converting to Windows DWORD.
	h, err := windows.OpenProcess(access, false, pid32)
	if err == nil {
		_ = windows.CloseHandle(h)
		return true
	}
	if err == windows.ERROR_ACCESS_DENIED {
		return true
	}
	return false
}

func tryExclusivePIDFileLock(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		if err == windows.ERROR_LOCK_VIOLATION {
			return ErrAnotherWizardRunning
		}
		return err
	}
	return nil
}

func unlockPIDFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
