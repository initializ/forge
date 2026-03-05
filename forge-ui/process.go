package forgeui

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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

// ProcessManager manages agent process lifecycles via `forge serve` commands.
type ProcessManager struct {
	mu      sync.Mutex
	exePath string
	ports   *PortAllocator
	broker  *SSEBroker
	// allocated tracks which ports were allocated by this PM so we can release them.
	allocated map[string]int
}

// NewProcessManager creates a ProcessManager.
func NewProcessManager(exePath string, broker *SSEBroker, basePort int) *ProcessManager {
	return &ProcessManager{
		exePath:   exePath,
		ports:     NewPortAllocator(basePort),
		broker:    broker,
		allocated: make(map[string]int),
	}
}

// Start launches an agent via `forge serve start`.
func (pm *ProcessManager) Start(agentID string, info *AgentInfo, passphrase string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	port := pm.ports.Allocate()
	pm.allocated[agentID] = port

	args := []string{"serve", "start", "--port", strconv.Itoa(port)}
	if len(info.Channels) > 0 {
		args = append(args, "--with", strings.Join(info.Channels, ","))
	}
	cmd := exec.Command(pm.exePath, args...)
	cmd.Dir = info.Directory

	if passphrase != "" {
		cmd.Env = append(cmd.Environ(), "FORGE_PASSPHRASE="+passphrase)
	}

	if err := cmd.Run(); err != nil {
		pm.ports.Release(port)
		delete(pm.allocated, agentID)

		info.Status = StateErrored
		info.Error = err.Error()
		pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: info})

		return fmt.Errorf("forge serve start failed: %w", err)
	}

	info.Status = StateRunning
	info.Port = port
	info.Error = ""
	pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: info})

	return nil
}

// Stop stops an agent via `forge serve stop`.
func (pm *ProcessManager) Stop(agentID string, agentDir string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	cmd := exec.Command(pm.exePath, "serve", "stop")
	cmd.Dir = agentDir

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("forge serve stop failed: %w", err)
	}

	if port, ok := pm.allocated[agentID]; ok {
		pm.ports.Release(port)
		delete(pm.allocated, agentID)
	}

	pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: &AgentInfo{
		ID:        agentID,
		Directory: agentDir,
		Status:    StateStopped,
	}})

	return nil
}

// StopAll is a no-op — agents intentionally survive UI shutdown.
func (pm *ProcessManager) StopAll() {
	// Agents are daemon processes that survive UI shutdown.
}
