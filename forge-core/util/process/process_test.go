package process

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestIsAlive_Self(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Errorf("IsAlive(self=%d) = false, want true", os.Getpid())
	}
}

// TestIsAlive_ChildAfterExit spawns a short-lived subprocess, waits for it
// to exit cleanly, and asserts IsAlive returns false for its PID. This is
// the deterministic way to exercise the "process is gone" branch on both
// platforms — picking a "high PID that probably isn't real" is unreliable.
func TestIsAlive_ChildAfterExit(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "exit", "0")
	} else {
		cmd = exec.Command("true")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting subprocess: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("waiting for subprocess: %v", err)
	}
	// Give the OS a beat to actually reap the PID. On most systems this
	// is instant, but Windows handle cleanup can briefly lag.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !IsAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("IsAlive(pid=%d) = true after Wait() returned, want false", pid)
}
