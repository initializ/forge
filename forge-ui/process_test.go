package forgeui

import (
	"testing"
)

func TestPortAllocator(t *testing.T) {
	pa := NewPortAllocator(9100)

	p1 := pa.Allocate()
	if p1 < 9100 {
		t.Errorf("first port = %d, want >= 9100", p1)
	}

	p2 := pa.Allocate()
	if p2 <= p1 {
		t.Errorf("second port = %d, want > %d", p2, p1)
	}

	// Release first port and re-allocate — should get it back.
	pa.Release(p1)
	p3 := pa.Allocate()
	if p3 != p1 {
		t.Errorf("after release, port = %d, want %d", p3, p1)
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

	// The allocated port should have been released (not in allocated map).
	pm.mu.Lock()
	_, tracked := pm.allocated["test-agent"]
	pm.mu.Unlock()
	if tracked {
		t.Error("expected agent port to be released from allocated map after failure")
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
