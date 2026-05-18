//go:build windows

package cmd

import (
	"os"
	"syscall"
)

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008} // DETACHED_PROCESS
}

func sendTermSignal(proc *os.Process) error {
	// Windows has no SIGTERM; terminate the process directly.
	return proc.Kill()
}

func sendKillSignal(proc *os.Process) error {
	return proc.Kill()
}
