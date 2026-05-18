//go:build windows

// Package process provides small, platform-aware utilities for interacting
// with OS processes by PID. See process_unix.go for the package overview.
package process

import "syscall"

// processQueryLimitedInfo is Windows' lightweight process access right
// (no PROCESS_VM_READ, etc.) — enough to call GetProcessTimes /
// QueryFullProcessImageName / etc. without elevated privileges. Granting
// PROCESS_QUERY_INFORMATION instead would also work but requires more
// rights than this check needs.
const processQueryLimitedInfo = 0x1000

// IsAlive reports whether a process with the given PID is currently running.
//
// On Windows, the Unix idiom os.Process.Signal(syscall.Signal(0)) does not
// work — Go's stdlib only knows how to translate os.Interrupt and os.Kill
// for Windows processes, so any other signal returns
// "operating system does not support signal". This always-error response
// makes Signal(0) useless as a liveness probe on Windows.
//
// Instead, open the process handle with PROCESS_QUERY_LIMITED_INFORMATION
// rights. OpenProcess fails only when the PID doesn't exist or the caller
// lacks rights even for the limited-info subset; both cases reasonably map
// to "not alive" for our use case (the forge daemon is the caller's child,
// so it always has rights to its own PID).
//
// The handle is closed immediately — we only care that OpenProcess
// succeeded, not what the handle exposes.
func IsAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(h)
	return true
}
