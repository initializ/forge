package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// daemonState is persisted in .forge/serve.json.
type daemonState struct {
	PID  int    `json:"pid"`
	Port int    `json:"port"`
	Host string `json:"host"`
}

var (
	servePort              int
	serveHost              string
	serveShutdownTimeout   time.Duration
	serveEnforceGuardrails bool
	serveModel             string
	serveProvider          string
	serveEnvFile           string
	serveWithChannels      string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Manage the agent as a background daemon",
	Long: `Manage the agent as a background daemon process.

When invoked without a subcommand, starts the daemon (equivalent to "forge serve start").

Subcommands:
  start   - Start the daemon (default)
  stop    - Stop a running daemon
  status  - Show daemon status
  logs    - Tail daemon logs

Examples:
  forge serve                         # Start daemon on 127.0.0.1:8080
  forge serve start --port 9090       # Start on custom port
  forge serve stop                    # Stop the daemon
  forge serve status                  # Show running status
  forge serve logs                    # View recent logs`,
	RunE: serveStartRun, // bare "forge serve" acts as "forge serve start"
}

var serveStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the agent daemon",
	RunE:  serveStartRun,
}

var serveStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running agent daemon",
	RunE:  serveStopRun,
}

var serveStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show agent daemon status",
	RunE:  serveStatusRun,
}

var serveLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Tail daemon log output",
	RunE:  serveLogsRun,
}

func registerServeFlags(cmd *cobra.Command) {
	cmd.Flags().IntVarP(&servePort, "port", "p", 8080, "HTTP server port")
	cmd.Flags().StringVar(&serveHost, "host", "127.0.0.1", "bind address (use 0.0.0.0 for containers)")
	cmd.Flags().DurationVar(&serveShutdownTimeout, "shutdown-timeout", 30*time.Second, "graceful shutdown timeout")
	cmd.Flags().BoolVar(&serveEnforceGuardrails, "enforce-guardrails", false, "enforce guardrail violations as errors")
	cmd.Flags().StringVar(&serveModel, "model", "", "override model name (sets MODEL_NAME env var)")
	cmd.Flags().StringVar(&serveProvider, "provider", "", "LLM provider (openai, anthropic, ollama)")
	cmd.Flags().StringVar(&serveEnvFile, "env", ".env", "path to .env file")
	cmd.Flags().StringVar(&serveWithChannels, "with", "", "comma-separated channel adapters to start (e.g. slack,telegram)")
}

func init() {
	registerServeFlags(serveCmd)
	registerServeFlags(serveStartCmd)

	serveCmd.AddCommand(serveStartCmd)
	serveCmd.AddCommand(serveStopCmd)
	serveCmd.AddCommand(serveStatusCmd)
	serveCmd.AddCommand(serveLogsCmd)
}

// stateFilePath returns the path to .forge/serve.json relative to the current directory.
func stateFilePath() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, ".forge", "serve.json")
}

// logFilePath returns the path to .forge/serve.log relative to the current directory.
func logFilePath() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, ".forge", "serve.log")
}

// readDaemonState reads the daemon state file and verifies the process is alive.
// Returns the state and true if the daemon is running, or zero state and false otherwise.
func readDaemonState(path string) (daemonState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return daemonState{}, false
	}

	var state daemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return daemonState{}, false
	}

	if state.PID <= 0 || !isProcessAlive(state.PID) {
		return daemonState{}, false
	}

	return state, true
}

// isProcessAlive checks whether a process with the given PID exists using signal(0).
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// serveStartRun starts the daemon by forking "forge run" in the background.
func serveStartRun(cmd *cobra.Command, args []string) error {
	statePath := stateFilePath()

	// Check if already running
	if state, running := readDaemonState(statePath); running {
		return fmt.Errorf("daemon already running (PID %d on %s:%d); use 'forge serve stop' first",
			state.PID, state.Host, state.Port)
	}

	// Call loadAndPrepareConfig in the parent (which has a TTY) so passphrase
	// prompting works and secrets are overlaid into the environment.
	if _, _, err := loadAndPrepareConfig(serveEnvFile); err != nil {
		return err
	}

	// Find our own executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding forge executable: %w", err)
	}

	// Build "forge run" args forwarding relevant flags
	runArgs := []string{"run",
		"--port", strconv.Itoa(servePort),
		"--host", serveHost,
		"--shutdown-timeout", serveShutdownTimeout.String(),
	}
	if serveEnforceGuardrails {
		runArgs = append(runArgs, "--enforce-guardrails")
	}
	if serveModel != "" {
		runArgs = append(runArgs, "--model", serveModel)
	}
	if serveProvider != "" {
		runArgs = append(runArgs, "--provider", serveProvider)
	}
	if serveEnvFile != ".env" {
		runArgs = append(runArgs, "--env", serveEnvFile)
	}
	if serveWithChannels != "" {
		runArgs = append(runArgs, "--with", serveWithChannels)
	}

	// Ensure .forge directory exists
	forgeDir := filepath.Dir(statePath)
	if err := os.MkdirAll(forgeDir, 0755); err != nil {
		return fmt.Errorf("creating .forge directory: %w", err)
	}

	// Open log file
	logPath := logFilePath()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}

	child := exec.Command(exePath, runArgs...)
	child.Stdout = logFile
	child.Stderr = logFile
	child.Env = os.Environ()
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("starting daemon: %w", err)
	}

	// Write state file
	state := daemonState{
		PID:  child.Process.Pid,
		Port: servePort,
		Host: serveHost,
	}
	stateData, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, stateData, 0644); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("writing state file: %w", err)
	}

	// Release the child process so it can run independently
	if err := child.Process.Release(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("releasing daemon process: %w", err)
	}

	_ = logFile.Close()

	fmt.Fprintf(os.Stderr, "Daemon started:\n")
	fmt.Fprintf(os.Stderr, "  PID:     %d\n", state.PID)
	fmt.Fprintf(os.Stderr, "  Listen:  %s:%d\n", state.Host, state.Port)
	fmt.Fprintf(os.Stderr, "  Logs:    %s\n", logPath)

	return nil
}

// serveStopRun stops the running daemon.
func serveStopRun(cmd *cobra.Command, args []string) error {
	statePath := stateFilePath()
	state, running := readDaemonState(statePath)
	if !running {
		fmt.Fprintln(os.Stderr, "No daemon is running.")
		// Clean up stale state file if it exists
		os.Remove(statePath) //nolint:errcheck
		return nil
	}

	proc, err := os.FindProcess(state.PID)
	if err != nil {
		os.Remove(statePath) //nolint:errcheck
		return fmt.Errorf("finding process %d: %w", state.PID, err)
	}

	// Send SIGTERM
	fmt.Fprintf(os.Stderr, "Stopping daemon (PID %d)...\n", state.PID)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		os.Remove(statePath) //nolint:errcheck
		return fmt.Errorf("sending SIGTERM: %w", err)
	}

	// Poll for exit (up to 10 seconds)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(state.PID) {
			os.Remove(statePath) //nolint:errcheck
			fmt.Fprintln(os.Stderr, "Daemon stopped.")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Fallback to SIGKILL
	fmt.Fprintln(os.Stderr, "Daemon did not stop in time, sending SIGKILL...")
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		os.Remove(statePath) //nolint:errcheck
		return fmt.Errorf("sending SIGKILL: %w", err)
	}

	os.Remove(statePath) //nolint:errcheck
	fmt.Fprintln(os.Stderr, "Daemon killed.")
	return nil
}

// serveStatusRun displays daemon status.
func serveStatusRun(cmd *cobra.Command, args []string) error {
	statePath := stateFilePath()
	logPath := logFilePath()
	state, running := readDaemonState(statePath)

	if !running {
		fmt.Fprintln(os.Stderr, "Status: not running")
		// Clean up stale state file if it exists
		os.Remove(statePath) //nolint:errcheck
		return nil
	}

	fmt.Fprintf(os.Stderr, "Status:  running\n")
	fmt.Fprintf(os.Stderr, "PID:     %d\n", state.PID)
	fmt.Fprintf(os.Stderr, "Listen:  %s:%d\n", state.Host, state.Port)
	fmt.Fprintf(os.Stderr, "Logs:    %s\n", logPath)

	// Try to hit the health endpoint for uptime
	healthURL := fmt.Sprintf("http://%s:%d/health", state.Host, state.Port)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL) //nolint:noctx
	if err == nil {
		defer resp.Body.Close() //nolint:errcheck
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Health:  %s\n", strings.TrimSpace(string(body)))
	} else {
		fmt.Fprintf(os.Stderr, "Health:  unreachable (%v)\n", err)
	}

	return nil
}

// serveLogsRun tails the last 100 lines of the daemon log.
func serveLogsRun(cmd *cobra.Command, args []string) error {
	logPath := logFilePath()

	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "No log file found. Is the daemon running?")
			return nil
		}
		return fmt.Errorf("reading log file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	start := 0
	if len(lines) > 100 {
		start = len(lines) - 100
	}

	for _, line := range lines[start:] {
		fmt.Println(line)
	}

	return nil
}
