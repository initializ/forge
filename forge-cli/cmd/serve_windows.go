//go:build windows

package cmd

import (
	"os"
	"syscall"
)

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008} // DETACHED_PROCESS
}

func isProcessAlive(pid int) bool {
	const processQueryLimitedInfo = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(h)
	return true
}

func sendTermSignal(proc *os.Process) error {
	// Windows has no SIGTERM; terminate the process directly.
	return proc.Kill()
}

func sendKillSignal(proc *os.Process) error {
	return proc.Kill()
}
