package forgeui

import (
	"context"
	"testing"
	"time"
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

func TestProcessManagerStartStop(t *testing.T) {
	broker := NewSSEBroker()
	ch := broker.Subscribe()
	defer broker.Unsubscribe(ch)

	started := make(chan struct{})
	stopped := make(chan struct{})

	mockStart := func(ctx context.Context, agentDir string, port int) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil
	}

	pm := NewProcessManager(mockStart, broker, 9100)

	info := &AgentInfo{
		ID:        "test-agent",
		Directory: "/tmp/test",
		Status:    StateStopped,
	}

	if err := pm.Start("test-agent", info); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for agent to start
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not start in time")
	}

	// Verify running state
	status := pm.Status("test-agent")
	if status != StateStarting && status != StateRunning {
		t.Errorf("status = %q, want starting or running", status)
	}

	// Should error on double start
	info2 := &AgentInfo{ID: "test-agent", Directory: "/tmp/test"}
	if err := pm.Start("test-agent", info2); err == nil {
		t.Error("expected error on double start")
	}

	// Stop
	if err := pm.Stop("test-agent"); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not stop in time")
	}

	// Wait for state to settle
	time.Sleep(100 * time.Millisecond)

	status = pm.Status("test-agent")
	if status != StateStopped {
		t.Errorf("after stop, status = %q, want stopped", status)
	}
}

func TestProcessManagerStopNotRunning(t *testing.T) {
	broker := NewSSEBroker()
	pm := NewProcessManager(nil, broker, 9100)

	if err := pm.Stop("nonexistent"); err == nil {
		t.Error("expected error stopping non-running agent")
	}
}
