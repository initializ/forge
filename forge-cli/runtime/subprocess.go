package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// SubprocessRuntime manages a child agent process and proxies A2A requests to it.
type SubprocessRuntime struct {
	entrypoint   string
	workDir      string
	env          map[string]string
	internalPort int
	logger       coreruntime.Logger

	mu  sync.Mutex
	cmd *exec.Cmd
}

// NewSubprocessRuntime creates a runtime that will start the given entrypoint
// command, passing PORT as an env var for the subprocess to listen on.
func NewSubprocessRuntime(entrypoint, workDir string, env map[string]string, logger coreruntime.Logger) *SubprocessRuntime {
	return &SubprocessRuntime{
		entrypoint: entrypoint,
		workDir:    workDir,
		env:        env,
		logger:     logger,
	}
}

// Start launches the subprocess and waits for it to become healthy.
func (s *SubprocessRuntime) Start(ctx context.Context) error {
	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("finding free port: %w", err)
	}
	s.internalPort = port

	fields := strings.Fields(s.entrypoint)
	if len(fields) == 0 {
		return fmt.Errorf("empty entrypoint")
	}

	s.mu.Lock()
	s.cmd = exec.CommandContext(ctx, fields[0], fields[1:]...)
	s.cmd.Dir = s.workDir

	// Build environment
	env := os.Environ()
	for k, v := range s.env {
		env = append(env, k+"="+v)
	}
	env = append(env, fmt.Sprintf("PORT=%d", s.internalPort))
	s.cmd.Env = env

	// Pipe stderr through logger
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	// Capture stdout too
	s.cmd.Stdout = os.Stdout

	if err := s.cmd.Start(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("starting subprocess: %w", err)
	}
	s.mu.Unlock()

	// Log stderr in background
	go s.pipeStderr(stderr)

	// Wait for subprocess to be healthy
	s.logger.Info("waiting for subprocess", map[string]any{
		"port":       s.internalPort,
		"entrypoint": s.entrypoint,
	})

	if err := s.waitForHealth(ctx); err != nil {
		s.Stop() //nolint:errcheck
		return fmt.Errorf("subprocess health check failed: %w", err)
	}

	s.logger.Info("subprocess is healthy", map[string]any{"port": s.internalPort})
	return nil
}

func (s *SubprocessRuntime) pipeStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		s.logger.Debug("subprocess", map[string]any{"stderr": scanner.Text()})
	}
}

func (s *SubprocessRuntime) waitForHealth(ctx context.Context) error {
	deadline := time.After(60 * time.Second)
	interval := 100 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for subprocess on port %d", s.internalPort)
		default:
		}

		if s.Healthy(ctx) {
			return nil
		}
		time.Sleep(interval)
		// Exponential backoff, cap at 2s
		if interval < 2*time.Second {
			interval = interval * 2
		}
	}
}

// Invoke sends a synchronous tasks/send request to the subprocess.
func (s *SubprocessRuntime) Invoke(ctx context.Context, taskID string, msg *a2a.Message) (*a2a.Task, error) {
	reqBody := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      taskID,
		Method:  "tasks/send",
		Params:  mustMarshal(a2a.SendTaskParams{ID: taskID, Message: *msg}),
	}

	data, _ := json.Marshal(reqBody)
	url := fmt.Sprintf("http://127.0.0.1:%d/", s.internalPort)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("proxy request: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp a2a.JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decoding proxy response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("subprocess error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	// Extract Task from result
	resultData, err := json.Marshal(rpcResp.Result)
	if err != nil {
		return nil, fmt.Errorf("marshalling result: %w", err)
	}
	var task a2a.Task
	if err := json.Unmarshal(resultData, &task); err != nil {
		return nil, fmt.Errorf("unmarshalling task: %w", err)
	}

	return &task, nil
}

// Stream sends a tasks/sendSubscribe request. If the subprocess returns SSE,
// events are streamed; otherwise the response is wrapped as a single item.
func (s *SubprocessRuntime) Stream(ctx context.Context, taskID string, msg *a2a.Message) (<-chan *a2a.Task, error) {
	reqBody := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      taskID,
		Method:  "tasks/sendSubscribe",
		Params:  mustMarshal(a2a.SendTaskParams{ID: taskID, Message: *msg}),
	}

	data, _ := json.Marshal(reqBody)
	url := fmt.Sprintf("http://127.0.0.1:%d/", s.internalPort)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("proxy request: %w", err)
	}

	ch := make(chan *a2a.Task, 8)

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		// Parse SSE events
		go func() {
			defer resp.Body.Close()
			defer close(ch)
			s.readSSEEvents(resp.Body, ch)
		}()
	} else {
		// Graceful degradation: wrap sync response
		go func() {
			defer resp.Body.Close()
			defer close(ch)

			var rpcResp a2a.JSONRPCResponse
			if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
				return
			}
			if rpcResp.Error != nil || rpcResp.Result == nil {
				return
			}
			resultData, _ := json.Marshal(rpcResp.Result)
			var task a2a.Task
			if json.Unmarshal(resultData, &task) == nil {
				ch <- &task
			}
		}()
	}

	return ch, nil
}

func (s *SubprocessRuntime) readSSEEvents(r io.Reader, ch chan<- *a2a.Task) {
	scanner := bufio.NewScanner(r)
	var dataLine string
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			dataLine = after
		} else if line == "" && dataLine != "" {
			var task a2a.Task
			if json.Unmarshal([]byte(dataLine), &task) == nil {
				ch <- &task
			}
			dataLine = ""
		}
	}
}

// Healthy checks if the subprocess is responding.
func (s *SubprocessRuntime) Healthy(ctx context.Context) bool {
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", s.internalPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Stop sends SIGTERM, waits 5s, then SIGKILL if needed.
func (s *SubprocessRuntime) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}

	// Send interrupt signal
	s.cmd.Process.Signal(os.Interrupt) //nolint:errcheck

	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		s.cmd.Process.Kill() //nolint:errcheck
		return nil
	}
}

// Restart stops and re-starts the subprocess.
func (s *SubprocessRuntime) Restart(ctx context.Context) error {
	s.logger.Info("restarting subprocess", nil)
	if err := s.Stop(); err != nil {
		s.logger.Warn("stop error during restart", map[string]any{"error": err.Error()})
	}
	return s.Start(ctx)
}

// findFreePort binds to port 0, reads the assigned port, and closes.
func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
