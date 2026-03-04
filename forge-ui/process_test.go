package forgeui

import (
	"testing"
)

func TestPortAllocator(t *testing.T) {
	pa := NewPortAllocator(9100)

	p1 := pa.Allocate()
	if p1 != 9100 {
		t.Errorf("first port = %d, want 9100", p1)
	}

	p2 := pa.Allocate()
	if p2 != 9101 {
		t.Errorf("second port = %d, want 9101", p2)
	}

	pa.Release(9100)
	p3 := pa.Allocate()
	if p3 != 9100 {
		t.Errorf("after release, port = %d, want 9100", p3)
	}
}

func TestProcessManagerStartExecError(t *testing.T) {
	broker := NewSSEBroker()
	ch := broker.Subscribe()
	defer broker.Unsubscribe(ch)

	// Use a non-existent binary so the exec.Command fails.
	pm := NewProcessManager("/nonexistent/binary", broker, 9100)

	info := &AgentInfo{
		ID:        "test-agent",
		Directory: t.TempDir(),
		Status:    StateStopped,
	}

	err := pm.Start("test-agent", info, "")
	if err == nil {
		t.Fatal("expected error from non-existent binary")
	}

	// Port should have been released.
	pm.ports.mu.Lock()
	_, used := pm.ports.used[9100]
	pm.ports.mu.Unlock()
	if used {
		t.Error("expected port 9100 to be released after failure")
	}
}

func TestProcessManagerStopExecError(t *testing.T) {
	broker := NewSSEBroker()
	pm := NewProcessManager("/nonexistent/binary", broker, 9100)

	err := pm.Stop("nonexistent", t.TempDir())
	if err == nil {
		t.Error("expected error stopping with non-existent binary")
	}
}

func TestProcessManagerStopAll(t *testing.T) {
	broker := NewSSEBroker()
	pm := NewProcessManager("/usr/bin/false", broker, 9100)

	// StopAll is a no-op — should not panic.
	pm.StopAll()
}
