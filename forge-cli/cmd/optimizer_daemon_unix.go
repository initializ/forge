//go:build !windows

package cmd

import "syscall"

// Process control for the background optimizer daemon on POSIX systems.

// processAlive reports whether pid names a live process (signal 0 probe).
func processAlive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// terminatePID asks the process to shut down (SIGTERM). No-op for pid <= 0.
func terminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// detachSysProcAttr starts the child in its own session so it outlives this
// shell (survives terminal close / SIGHUP).
func detachSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
