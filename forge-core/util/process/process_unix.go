//go:build !windows

// Package process provides small, platform-aware utilities for interacting
// with OS processes by PID. Today it ships a single function — IsAlive —
// because the same Unix idiom (Signal(0)) silently fails on Windows and was
// duplicated across forge-cli and forge-ui with the Windows variant missing.
// See issue #59.
package process

import (
	"os"
	"syscall"
)

// IsAlive reports whether a process with the given PID is currently running.
//
// On Unix, this calls FindProcess and sends a null signal (signal 0), which
// the kernel treats as a permission/existence check without delivering an
// actual signal. The OS returns success when the process exists, even if the
// caller lacks permission to signal it.
//
// Any error from either step is treated as "not alive" so callers can use a
// simple bool branch.
func IsAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
