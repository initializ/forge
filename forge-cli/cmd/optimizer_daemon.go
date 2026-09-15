package cmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// optimizerStartUI, when set, also brings up the forge dashboard and opens the
// Optimizer view after starting the daemon.
var optimizerStartUI bool

// The daemon commands make the optimizer an always-on background proxy that
// every `claude` — in any terminal — routes through, without per-session
// wrapping. The key move is that `start` writes ANTHROPIC_BASE_URL into Claude
// Code's own ~/.claude/settings.json `env` (which it applies to every session),
// so the export is inherited via config rather than pushed into shells. `stop`
// reverts it precisely, so `claude` never ends up pointed at a dead proxy.

// claudeSettingsPath is Claude Code's user settings file.
func claudeSettingsPath() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".claude", "settings.json")
	}
	return filepath.Join(".claude", "settings.json")
}

// optDaemonStatePath records what `start` changed, so `stop` can revert exactly.
func optDaemonStatePath() string { return forgeFile("optimizer-daemon.json") }

type optDaemonState struct {
	Listen         string  `json:"listen"`
	PID            int     `json:"pid,omitempty"`
	Spawned        bool    `json:"spawned"`       // true if we launched it (so stop kills it)
	PrevBaseURL    *string `json:"prev_base_url"` // nil = key was absent before start
	PrevToolSearch *string `json:"prev_tool_search"`
	MCPRegistered  bool    `json:"mcp_registered"`
}

var optimizerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a persistent background optimizer that every `claude` routes through",
	Long: `Launches a detached optimizer proxy (compression + memory ON) and wires it
into Claude Code's user settings so EVERY ` + "`claude`" + ` — in any terminal, without
` + "`forge optimizer claude`" + ` — routes through it. It:

  - starts the proxy in the background (survives this shell),
  - merges ANTHROPIC_BASE_URL + ENABLE_TOOL_SEARCH into ~/.claude/settings.json,
  - registers the context_expand MCP server.

Run ` + "`forge optimizer stop`" + ` to shut it down and revert the settings.`,
	RunE: runOptimizerStart,
}

var optimizerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background optimizer and revert Claude Code settings",
	RunE:  runOptimizerStop,
}

var optimizerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the background optimizer is running and wired into Claude Code",
	RunE:  runOptimizerStatus,
}

func init() {
	optimizerStartCmd.Flags().BoolVar(&optimizerStartUI, "ui", false, "also open the forge dashboard on the Optimizer view")
	optimizerCmd.AddCommand(optimizerStartCmd)
	optimizerCmd.AddCommand(optimizerStopCmd)
	optimizerCmd.AddCommand(optimizerStatusCmd)
}

func optDaemonLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func runOptimizerStart(_ *cobra.Command, _ []string) error {
	listen := resolveListen()
	logger := optDaemonLogger()

	running, ours := probeExistingOptimizer(listen)
	if running && !ours {
		return fmt.Errorf("address %s is already in use by a non-optimizer service; choose another with --listen", listen)
	}

	st := optDaemonState{Listen: listen}
	if running && ours {
		fmt.Printf("forge optimizer already running at http://%s — adopting it\n", listen)
	} else {
		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locating forge binary: %w", err)
		}
		logPath := forgeFile("optimizer.log")
		_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
		logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("opening daemon log: %w", err)
		}
		defer func() { _ = logf.Close() }()

		// Detached child running the standalone proxy with compression + memory.
		child := exec.Command(self, "optimizer", "--compress", "--memory", "--listen", listen, "--quiet") //nolint:gosec // self-exec, fixed args
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}                                            // survive this shell
		child.Stdin = nil
		child.Stdout = logf
		child.Stderr = logf
		if err := child.Start(); err != nil {
			return fmt.Errorf("starting optimizer daemon: %w", err)
		}
		st.PID = child.Process.Pid
		st.Spawned = true
		_ = child.Process.Release()

		if err := waitListenReady(listen, 8*time.Second); err != nil {
			return fmt.Errorf("optimizer daemon did not come up on %s (see %s): %w", listen, logPath, err)
		}
		fmt.Printf("forge optimizer started (pid %d) at http://%s → logs %s\n", st.PID, listen, logPath)
	}

	// Wire Claude Code settings so every `claude` inherits the base URL.
	prevBase, prevTS, err := wireClaudeSettings(listen)
	if err != nil {
		logger.Warn("could not update ~/.claude/settings.json; set ANTHROPIC_BASE_URL yourself", "error", err.Error())
	} else {
		st.PrevBaseURL, st.PrevToolSearch = prevBase, prevTS
		fmt.Printf("wired %s: ANTHROPIC_BASE_URL=http://%s (every `claude` now routes through it)\n", claudeSettingsPath(), listen)
	}

	// Register context_expand over MCP.
	if bin, e := exec.LookPath("claude"); e == nil {
		registerOptimizerMCP(bin, listen, logger)
		st.MCPRegistered = true
	}

	if err := writeOptDaemonState(st); err != nil {
		logger.Warn("could not persist daemon state (stop may not fully revert)", "error", err.Error())
	}

	if optimizerStartUI {
		if self, e := os.Executable(); e == nil {
			if base := ensureForgeUI(self, logger); base != "" {
				url := base + "/#/optimizer"
				fmt.Printf("opening dashboard → %s\n", url)
				openURL(url)
			}
		}
	}

	fmt.Println("Run `claude` in any terminal — it will use the optimizer. `forge optimizer stop` to undo.")
	return nil
}

// ensureForgeUI returns the base URL of a running forge dashboard, launching a
// detached one on :4200 if none is up. Returns "" on failure.
func ensureForgeUI(self string, logger *slog.Logger) string {
	const base = "http://127.0.0.1:4200"
	client := &http.Client{Timeout: 600 * time.Millisecond}
	if resp, err := client.Get(base + "/api/health"); err == nil {
		_ = resp.Body.Close()
		return base // already running
	}
	logPath := forgeFile("optimizer-ui.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logger.Warn("could not open UI log", "error", err.Error())
		return ""
	}
	child := exec.Command(self, "ui", "--port", "4200", "--no-open") //nolint:gosec // self-exec, fixed args
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdin = nil
	child.Stdout = logf
	child.Stderr = logf
	if err := child.Start(); err != nil {
		_ = logf.Close()
		logger.Warn("could not start forge ui", "error", err.Error())
		return ""
	}
	_ = child.Process.Release()
	// Wait briefly for readiness.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := client.Get(base + "/api/health"); err == nil {
			_ = resp.Body.Close()
			return base
		}
		time.Sleep(150 * time.Millisecond)
	}
	logger.Warn("forge ui did not become ready", "log", logPath)
	return base // best-effort: still try to open it
}

// openURL opens a URL in the default browser (best-effort, cross-platform).
func openURL(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}

func runOptimizerStop(_ *cobra.Command, _ []string) error {
	logger := optDaemonLogger()
	st, err := readOptDaemonState()
	if err != nil {
		// No state file — best-effort cleanup so we never leave claude pointed at
		// a dead proxy: unwire settings unconditionally and deregister MCP.
		fmt.Println("no daemon state found; reverting Claude Code settings best-effort")
		_ = restoreClaudeSettings(nil, nil) // nil,nil → delete the keys we manage
		deregisterOptimizerMCP(logger)
		return nil
	}

	if err := restoreClaudeSettings(st.PrevBaseURL, st.PrevToolSearch); err != nil {
		logger.Warn("reverting ~/.claude/settings.json failed", "error", err.Error())
	} else {
		fmt.Println("reverted ~/.claude/settings.json")
	}
	if st.MCPRegistered {
		deregisterOptimizerMCP(logger)
	}
	if st.Spawned && st.PID > 0 {
		if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil {
			logger.Warn("could not signal daemon (already gone?)", "pid", st.PID, "error", err.Error())
		} else {
			fmt.Printf("stopped optimizer daemon (pid %d)\n", st.PID)
		}
	} else {
		fmt.Println("did not start the running proxy myself — leaving it up (settings reverted)")
	}
	_ = os.Remove(optDaemonStatePath())
	return nil
}

func runOptimizerStatus(_ *cobra.Command, _ []string) error {
	listen := resolveListen()
	st, _ := readOptDaemonState()
	if st.Listen != "" {
		listen = st.Listen
	}
	running, ours := probeExistingOptimizer(listen)

	fmt.Printf("forge optimizer status\n")
	switch {
	case running && ours:
		fmt.Printf("  proxy       : RUNNING at http://%s\n", listen)
	case running && !ours:
		fmt.Printf("  proxy       : address %s in use by a NON-optimizer service\n", listen)
	default:
		fmt.Printf("  proxy       : not running (expected http://%s)\n", listen)
	}
	if st.PID > 0 {
		alive := syscall.Kill(st.PID, 0) == nil
		fmt.Printf("  daemon pid  : %d (%s)\n", st.PID, map[bool]string{true: "alive", false: "not found"}[alive])
	}
	wired := settingsBaseURL()
	if wired != "" {
		fmt.Printf("  claude env  : ANTHROPIC_BASE_URL=%s (via %s)\n", wired, claudeSettingsPath())
	} else {
		fmt.Printf("  claude env  : not wired in %s\n", claudeSettingsPath())
	}
	fmt.Printf("  mcp         : %s\n", map[bool]string{true: "registered", false: "not (via state)"}[st.MCPRegistered])
	return nil
}

// --- Claude settings.json merge / revert ---

func readClaudeSettings() (map[string]any, error) {
	b, err := os.ReadFile(claudeSettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", claudeSettingsPath(), err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func writeClaudeSettings(m map[string]any) error {
	path := claudeSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// captureStr returns a pointer to the string value if present, else nil.
func captureStr(env map[string]any, key string) *string {
	if v, ok := env[key]; ok {
		if s, ok := v.(string); ok {
			return &s
		}
	}
	return nil
}

// wireClaudeSettings sets ANTHROPIC_BASE_URL + ENABLE_TOOL_SEARCH under `env`,
// returning the prior values (nil = key was absent) so stop can revert exactly.
func wireClaudeSettings(listen string) (*string, *string, error) {
	m, err := readClaudeSettings()
	if err != nil {
		return nil, nil, err
	}
	env, _ := m["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	prevBase := captureStr(env, "ANTHROPIC_BASE_URL")
	prevTS := captureStr(env, "ENABLE_TOOL_SEARCH")
	env["ANTHROPIC_BASE_URL"] = "http://" + listen
	env["ENABLE_TOOL_SEARCH"] = "true"
	m["env"] = env
	if err := writeClaudeSettings(m); err != nil {
		return nil, nil, err
	}
	return prevBase, prevTS, nil
}

// restoreClaudeSettings puts ANTHROPIC_BASE_URL/ENABLE_TOOL_SEARCH back to prev
// (nil prev → remove the key).
func restoreClaudeSettings(prevBase, prevTS *string) error {
	m, err := readClaudeSettings()
	if err != nil {
		return err
	}
	env, _ := m["env"].(map[string]any)
	if env == nil {
		return nil // nothing to restore
	}
	setOrDelete(env, "ANTHROPIC_BASE_URL", prevBase)
	setOrDelete(env, "ENABLE_TOOL_SEARCH", prevTS)
	if len(env) == 0 {
		delete(m, "env")
	} else {
		m["env"] = env
	}
	return writeClaudeSettings(m)
}

func setOrDelete(env map[string]any, key string, prev *string) {
	if prev == nil {
		delete(env, key)
	} else {
		env[key] = *prev
	}
}

// settingsBaseURL returns the currently-wired ANTHROPIC_BASE_URL, or "".
func settingsBaseURL() string {
	m, err := readClaudeSettings()
	if err != nil {
		return ""
	}
	if env, ok := m["env"].(map[string]any); ok {
		if s, ok := env["ANTHROPIC_BASE_URL"].(string); ok {
			return s
		}
	}
	return ""
}

// deregisterOptimizerMCP removes the context_expand MCP registration.
func deregisterOptimizerMCP(logger *slog.Logger) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return
	}
	if err := exec.Command(bin, "mcp", "remove", mcpServerName, "--scope", "user").Run(); err != nil { //nolint:gosec // fixed args
		logger.Debug("could not deregister MCP", "error", err.Error())
	}
}

// --- daemon state ---

func writeOptDaemonState(st optDaemonState) error {
	if err := os.MkdirAll(filepath.Dir(optDaemonStatePath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(optDaemonStatePath(), b, 0o600)
}

func readOptDaemonState() (optDaemonState, error) {
	var st optDaemonState
	b, err := os.ReadFile(optDaemonStatePath())
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}
