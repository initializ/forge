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

// processSynchronize (SYNCHRONIZE) is required to wait on a process
// handle, which is how liveness is actually determined below.
const processSynchronize = 0x00100000

// waitTimeout (WAIT_TIMEOUT) is WaitForSingleObject's answer when the
// handle is NOT signaled — i.e. the process has not exited.
const waitTimeout = 0x00000102

// IsAlive reports whether a process with the given PID is currently running.
//
// On Windows, the Unix idiom os.Process.Signal(syscall.Signal(0)) does not
// work — Go's stdlib only knows how to translate os.Interrupt and os.Kill
// for Windows processes, so any other signal returns
// "operating system does not support signal". This always-error response
// makes Signal(0) useless as a liveness probe on Windows.
//
// Handle openability is NOT liveness either: Windows keeps a terminated
// process's kernel object alive as long as any handle to it remains open
// (so callers can still read its exit code), and OpenProcess succeeds
// against that object. A caller that holds a handle while polling — as
// `forge serve stop` does via os.FindProcess — would therefore see its
// own dead child reported as running forever, wait out the full timeout,
// and then fail with "Access is denied" trying to kill it twice.
//
// So open the handle and wait on it with a zero timeout instead. A
// process handle becomes signaled exactly when the process exits, which
// is unambiguous: signaled means exited, WAIT_TIMEOUT means still
// running. (GetExitCodeProcess is the other option, but its STILL_ACTIVE
// sentinel is 259 and collides with a genuine exit code of 259.)
func IsAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInfo|processSynchronize, false, uint32(pid))
	if err != nil {
		// PID doesn't exist, or we lack even limited-info rights —
		// both map to "not alive" for our use case (the forge daemon
		// is the caller's child, so rights are never the issue).
		return false
	}
	defer syscall.CloseHandle(h) //nolint:errcheck

	event, err := syscall.WaitForSingleObject(h, 0)
	if err != nil {
		// WAIT_FAILED. Report not-alive rather than pinning a caller
		// in a poll loop it can never exit.
		return false
	}
	return event == waitTimeout
}
