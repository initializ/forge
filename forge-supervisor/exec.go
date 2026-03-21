package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// ExecAgent forks and executes the agent process as UID 1000.
// The supervisor stays as UID 0 so its own traffic is NOT redirected
// by the iptables OUTPUT chain (which targets UID 1000 only).
// Returns the *os.Process of the child.
func ExecAgent(args []string) (*os.Process, error) {
	path, err := exec.LookPath(args[0])
	if err != nil {
		return nil, fmt.Errorf("lookpath %q: %w", args[0], err)
	}

	cmd := exec.Command(path, args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Set UID/GID on the child — supervisor stays as UID 0.
	// iptables redirects only UID 1000 traffic, so supervisor is unaffected.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: 1000, Gid: 1000},
		Setsid:     true,
		// Setctty only when stdin is a TTY (containers may not have one)
		Setctty: isStdinTTY(),
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	log.Printf("INFO: started agent (PID %d) as UID 1000: %s", cmd.Process.Pid, path)
	return cmd.Process, nil
}

// ForwardSignal forwards a signal to the process.
func ForwardSignal(pid int, sig syscall.Signal) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		log.Printf("ERROR: find process %d: %v", pid, err)
		return
	}
	if err := proc.Signal(sig); err != nil {
		log.Printf("ERROR: signal %d to %d: %v", sig, pid, err)
	}
}

// isStdinTTY returns true if stdin is a terminal.
func isStdinTTY() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	return err == nil
}
