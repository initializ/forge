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

	info := &AgentInfo{
		ID:        "nonexistent",
		Directory: t.TempDir(),
		Status:    StateRunning,
		Port:      9100,
	}

	err := pm.Stop("nonexistent", info)
	if err == nil {
		t.Error("expected error stopping with non-existent binary")
	}

	// A failed stop must roll back, not leave the card on "stopping".
	if info.Status != StateRunning {
		t.Errorf("status = %q, want %q after failed stop", info.Status, StateRunning)
	}
	if info.Port != 9100 {
		t.Errorf("port = %d, want 9100 preserved after failed stop", info.Port)
	}
	if info.Error == "" {
		t.Error("expected Error to be populated after failed stop")
	}
}

// Stop must emit a "stopping" event BEFORE it blocks on `forge serve stop`,
// and every event must carry a full record — a stub would blank the card's
// metadata in the dashboard.
func TestProcessManagerStopBroadcastsStoppingFirst(t *testing.T) {
	broker := NewSSEBroker()
	ch := broker.Subscribe()
	defer broker.Unsubscribe(ch)

	pm := NewProcessManager("/nonexistent/binary", broker, 9100)

	info := &AgentInfo{
		ID:        "test-agent",
		Version:   "1.2.3",
		Framework: "langchain",
		Channels:  []string{"slack"},
		Directory: t.TempDir(),
		Status:    StateRunning,
		Port:      9100,
	}

	_ = pm.Stop("test-agent", info)

	first := <-ch
	got, ok := first.Data.(*AgentInfo)
	if !ok {
		t.Fatalf("event data type = %T, want *AgentInfo", first.Data)
	}
	if got.Status != StateStopping {
		t.Errorf("first event status = %q, want %q", got.Status, StateStopping)
	}
	if got.Version != "1.2.3" || got.Framework != "langchain" || len(got.Channels) != 1 {
		t.Errorf("first event dropped metadata: %+v", got)
	}
	// The event must be a snapshot, not the live pointer — otherwise
	// Stop's later mutations rewrite an already-queued event.
	if got == info {
		t.Error("event aliases the caller's AgentInfo; broadcast a copy")
	}
}

func TestProcessManagerStopAll(t *testing.T) {
	broker := NewSSEBroker()
	pm := NewProcessManager("/usr/bin/false", broker, 9100)

	// StopAll is a no-op — should not panic.
	pm.StopAll()
}
