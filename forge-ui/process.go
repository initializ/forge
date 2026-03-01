package forgeui

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PortAllocator manages port assignment for agent processes.
type PortAllocator struct {
	mu       sync.Mutex
	basePort int
	used     map[int]struct{}
}

// NewPortAllocator creates a PortAllocator starting from basePort.
func NewPortAllocator(basePort int) *PortAllocator {
	return &PortAllocator{
		basePort: basePort,
		used:     make(map[int]struct{}),
	}
}

// Allocate returns the next available port.
func (pa *PortAllocator) Allocate() int {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	port := pa.basePort
	for {
		if _, ok := pa.used[port]; !ok {
			pa.used[port] = struct{}{}
			return port
		}
		port++
	}
}

// Release frees a port for reuse.
func (pa *PortAllocator) Release(port int) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	delete(pa.used, port)
}

// managedAgent tracks a running agent's context and cancel func.
type managedAgent struct {
	cancel context.CancelFunc
	port   int
}

// ProcessManager manages agent process lifecycles.
type ProcessManager struct {
	mu        sync.RWMutex
	startFunc AgentStartFunc
	agents    map[string]*managedAgent
	states    map[string]*AgentInfo
	ports     *PortAllocator
	broker    *SSEBroker
}

// NewProcessManager creates a ProcessManager.
func NewProcessManager(startFunc AgentStartFunc, broker *SSEBroker, basePort int) *ProcessManager {
	return &ProcessManager{
		startFunc: startFunc,
		agents:    make(map[string]*managedAgent),
		states:    make(map[string]*AgentInfo),
		ports:     NewPortAllocator(basePort),
		broker:    broker,
	}
}

// Start launches an agent. Returns an error if the agent is already running.
func (pm *ProcessManager) Start(agentID string, info *AgentInfo) error {
	pm.mu.Lock()
	if _, ok := pm.agents[agentID]; ok {
		pm.mu.Unlock()
		return fmt.Errorf("agent %s is already running", agentID)
	}

	port := pm.ports.Allocate()
	ctx, cancel := context.WithCancel(context.Background())

	pm.agents[agentID] = &managedAgent{cancel: cancel, port: port}

	// Update state
	now := time.Now()
	info.Status = StateStarting
	info.Port = port
	info.Error = ""
	info.StartedAt = &now
	pm.states[agentID] = info
	pm.mu.Unlock()

	pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: info})

	// Launch in goroutine — startFunc blocks until agent exits
	go func() {
		// Brief delay to allow status propagation, then set running
		time.Sleep(500 * time.Millisecond)
		pm.mu.Lock()
		if s, ok := pm.states[agentID]; ok && s.Status == StateStarting {
			s.Status = StateRunning
			pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: s})
		}
		pm.mu.Unlock()

		err := pm.startFunc(ctx, info.Directory, port)

		pm.mu.Lock()
		delete(pm.agents, agentID)
		pm.ports.Release(port)

		if s, ok := pm.states[agentID]; ok {
			s.Port = 0
			s.StartedAt = nil
			if err != nil && ctx.Err() == nil {
				// Agent exited with error (not from cancellation)
				s.Status = StateErrored
				s.Error = err.Error()
			} else {
				s.Status = StateStopped
				s.Error = ""
			}
			pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: s})
		}
		pm.mu.Unlock()
	}()

	return nil
}

// Stop signals an agent to stop.
func (pm *ProcessManager) Stop(agentID string) error {
	pm.mu.Lock()
	managed, ok := pm.agents[agentID]
	if !ok {
		pm.mu.Unlock()
		return fmt.Errorf("agent %s is not running", agentID)
	}

	if s, ok := pm.states[agentID]; ok {
		s.Status = StateStopping
		pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: s})
	}
	pm.mu.Unlock()

	managed.cancel()
	return nil
}

// GetPort returns the port of a running agent.
func (pm *ProcessManager) GetPort(agentID string) (int, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if a, ok := pm.agents[agentID]; ok {
		return a.port, true
	}
	return 0, false
}

// Status returns the current state of an agent.
func (pm *ProcessManager) Status(agentID string) ProcessState {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if s, ok := pm.states[agentID]; ok {
		return s.Status
	}
	return StateStopped
}

// GetState returns a copy of the agent's state info, or nil.
func (pm *ProcessManager) GetState(agentID string) *AgentInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if s, ok := pm.states[agentID]; ok {
		cp := *s
		return &cp
	}
	return nil
}

// MergeState merges process manager state into a discovered agent map.
func (pm *ProcessManager) MergeState(agents map[string]*AgentInfo) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for id, state := range pm.states {
		if agent, ok := agents[id]; ok {
			agent.Status = state.Status
			agent.Port = state.Port
			agent.Error = state.Error
			agent.StartedAt = state.StartedAt
		}
	}
}

// StopAll stops all running agents.
func (pm *ProcessManager) StopAll() {
	pm.mu.Lock()
	agents := make(map[string]*managedAgent, len(pm.agents))
	for id, a := range pm.agents {
		agents[id] = a
	}
	pm.mu.Unlock()

	for _, a := range agents {
		a.cancel()
	}
}
