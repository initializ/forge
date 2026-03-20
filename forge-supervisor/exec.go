package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// ExecAgent forks and executes the agent process.
// Returns the *os.Process of the child.
func ExecAgent(args []string) (*os.Process, error) {
	// Look up the binary
	path, err := exec.LookPath(args[0])
	if err != nil {
		return nil, fmt.Errorf("lookpath %q: %w", args[0], err)
	}

	// Fork/exec
	cmd := exec.Command(path, args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setctty: true,
		Setsid:  true,
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	log.Printf("INFO: started agent (PID %d): %s", cmd.Process.Pid, path)
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
