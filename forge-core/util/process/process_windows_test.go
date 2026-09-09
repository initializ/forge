//go:build windows

package process

import (
	"os/exec"
	"syscall"
	"testing"
)

// Windows keeps a terminated process's kernel object alive while any
// handle to it stays open, so OpenProcess still succeeds against a dead
// PID. `forge serve stop` holds exactly such a handle (from
// os.FindProcess) while it polls IsAlive, which made an
// openability-based check report its own dead child as running until the
// timeout expired — then fail with "Access is denied" on the second kill.
//
// TestIsAlive_ChildAfterExit does not cover this: cmd.Wait() closes Go's
// handle, releasing the object before the check runs. The handle must be
// held across the kill to reproduce it.
func TestIsAlive_FalseWhileHandleHeld(t *testing.T) {
	// ping runs long enough to be observed alive and needs no stdin
	// (timeout/pause both fail when stdin isn't a console).
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting subprocess: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Mirror os.FindProcess: an independent handle held across the kill.
	h, err := syscall.OpenProcess(processQueryLimitedInfo|processSynchronize, false, uint32(pid))
	if err != nil {
		t.Fatalf("OpenProcess: %v", err)
	}
	defer syscall.CloseHandle(h) //nolint:errcheck

	if !IsAlive(pid) {
		t.Fatalf("IsAlive(pid=%d) = false while running, want true", pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// h is still open, so the PID remains openable. IsAlive must not be
	// fooled by that.
	if IsAlive(pid) {
		t.Errorf("IsAlive(pid=%d) = true for a terminated process while a handle is held, want false", pid)
	}
}
