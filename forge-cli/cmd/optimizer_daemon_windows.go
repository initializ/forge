//go:build windows

package cmd

import (
	"os"
	"syscall"
)

// Process control for the background optimizer daemon on Windows, where POSIX
// signals aren't available — we use process handles instead.

// processAlive reports whether pid names a live process. On Windows
// os.FindProcess opens the process handle and fails if it no longer exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}

// terminatePID stops the process. Windows has no SIGTERM, so this is a hard
// TerminateProcess via os.Process.Kill. No-op for pid <= 0.
func terminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// detachSysProcAttr starts the child detached from this console
// (DETACHED_PROCESS) so it outlives the launching shell.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008} // DETACHED_PROCESS
}
