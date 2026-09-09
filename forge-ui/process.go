package forgeui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/util/process"
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

// Allocate returns the next available port. It verifies the port is actually
// free (not just absent from the used map) by attempting a TCP listen. This
// prevents collisions with externally-started agents or other processes that
// the PortAllocator doesn't know about (e.g., after a UI restart).
func (pa *PortAllocator) Allocate() int {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	port := pa.basePort
	for {
		if _, ok := pa.used[port]; !ok {
			if portFree(port) {
				pa.used[port] = struct{}{}
				return port
			}
			// Port is in use by another process; mark it and skip.
			pa.used[port] = struct{}{}
		}
		port++
	}
}

// portFree checks whether a TCP port is available by attempting to listen on it.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
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

// broadcastStatus emits a snapshot of info rather than the pointer itself.
// SSEBroker queues events and handleSSE marshals them when it drains the
// channel, so sharing the pointer would let a later mutation rewrite an
// already-queued event — a "stopping" event would serialize as "stopped".
//
// The snapshot is a SHALLOW copy: it decouples the scalar fields this package
// mutates (Status, Port, Error), but Tools, Channels and DeniedChannels still
// alias the caller's backing arrays, and StartedAt stays a shared pointer.
// That is safe only because Start/Stop never write through them — they are
// populated once by Scanner.scanDir and read-only thereafter. Mutating a slice
// element in place (rather than replacing the slice) would reintroduce the
// aliasing bug for that field, so deep-copy here if that ever changes.
func (pm *ProcessManager) broadcastStatus(info *AgentInfo) {
	snapshot := *info
	pm.broker.Broadcast(SSEEvent{Type: "agent_status", Data: &snapshot})
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

	// Capture stderr so we can surface error details to the UI.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if passphrase != "" {
		cmd.Env = append(cmd.Environ(), "FORGE_PASSPHRASE="+passphrase)
	}

	if err := cmd.Run(); err != nil {
		pm.ports.Release(port)
		delete(pm.allocated, agentID)

		// Build a useful error message from stderr output.
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}

		info.Status = StateErrored
		info.Error = errMsg
		pm.broadcastStatus(info)

		return fmt.Errorf("forge serve start failed: %s", errMsg)
	}

	// Verify the daemon actually started by probing the port.
	// forge serve start forks a child process — the child may crash
	// immediately after the parent returns success.
	if !pm.waitForPort(info.Directory, port) {
		pm.ports.Release(port)
		delete(pm.allocated, agentID)

		// Read serve.log for diagnostics.
		errMsg := pm.readServeLogs(info.Directory)
		if errMsg == "" {
			errMsg = fmt.Sprintf("agent process exited immediately (port %d never became reachable)", port)
		}

		info.Status = StateErrored
		info.Error = errMsg
		pm.broadcastStatus(info)

		return fmt.Errorf("agent failed to start: %s", errMsg)
	}

	info.Status = StateRunning
	info.Port = port
	info.Error = ""
	pm.broadcastStatus(info)

	return nil
}

// waitForPort polls the TCP port for up to 5 seconds waiting for the
// daemon's HTTP server to become reachable. It also checks PID liveness
// so we fail fast when the child process crashes during startup.
func (pm *ProcessManager) waitForPort(agentDir string, port int) bool {
	deadline := time.Now().Add(5 * time.Second)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Give the child process a moment to start (or crash).
	time.Sleep(500 * time.Millisecond)

	// Read PID from serve.json for liveness checks.
	pid := pm.readServePID(agentDir)

	for time.Now().Before(deadline) {
		// Fast-fail: if the child process already exited, don't keep polling.
		if pid > 0 && !process.IsAlive(pid) {
			return false
		}

		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}

		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// readServePID reads the daemon PID from .forge/serve.json.
func (pm *ProcessManager) readServePID(agentDir string) int {
	data, err := os.ReadFile(filepath.Join(agentDir, ".forge", "serve.json"))
	if err != nil {
		return 0
	}
	var state struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(data, &state) != nil {
		return 0
	}
	return state.PID
}

// readServeLogs reads .forge/serve.log and extracts the error message.
// It looks for cobra "Error:" lines first, then falls back to the last
// few non-empty lines. This avoids returning cobra usage/help text that
// gets dumped after the actual error.
func (pm *ProcessManager) readServeLogs(agentDir string) string {
	logPath := filepath.Join(agentDir, ".forge", "serve.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	// Look for "Error:" lines (cobra format) — return the last one found.
	var lastError string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Error:") {
			lastError = strings.TrimPrefix(trimmed, "Error: ")
		}
	}
	if lastError != "" {
		return lastError
	}

	// Fallback: return the last 5 non-empty lines.
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < 5; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			tail = append([]string{t}, tail...)
		}
	}
	return strings.Join(tail, "\n")
}

// Stop stops an agent via `forge serve stop`.
//
// Takes the full *AgentInfo so every broadcast carries a complete record:
// fields without omitempty marshal as zero values, and the dashboard merges
// events by object spread, so a stub event blanks the card's metadata.
func (pm *ProcessManager) Stop(agentID string, info *AgentInfo) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Announce before the blocking cmd.Run() — the HTTP handler won't
	// respond until it returns, so this is the only thing that can flip
	// the card to "stopping". SSE is served on its own goroutine.
	prevStatus, prevPort := info.Status, info.Port
	info.Status = StateStopping
	pm.broadcastStatus(info)

	cmd := exec.Command(pm.exePath, "serve", "stop")
	cmd.Dir = info.Directory

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}

		// Roll back, or the card stays wedged on "stopping" with its
		// buttons disabled.
		info.Status = prevStatus
		info.Port = prevPort
		info.Error = errMsg
		pm.broadcastStatus(info)

		return fmt.Errorf("forge serve stop failed: %s", errMsg)
	}

	if port, ok := pm.allocated[agentID]; ok {
		pm.ports.Release(port)
		delete(pm.allocated, agentID)
	}

	// Clear the port — otherwise a stopped card still shows a port tag.
	info.Status = StateStopped
	info.Port = 0
	info.Error = ""
	pm.broadcastStatus(info)

	return nil
}

// StopAll is a no-op — agents intentionally survive UI shutdown.
func (pm *ProcessManager) StopAll() {
	// Agents are daemon processes that survive UI shutdown.
}
