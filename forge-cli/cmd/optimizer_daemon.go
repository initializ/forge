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
	"strconv"
	"strings"
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
	Spawned        bool    `json:"spawned"`            // true if we launched it (so stop kills it)
	Upstream       string  `json:"upstream,omitempty"` // upstream we handed the child (empty = child default)
	PrevBaseURL    *string `json:"prev_base_url"`      // nil = key was absent before start
	PrevToolSearch *string `json:"prev_tool_search"`
	MCPRegistered  bool    `json:"mcp_registered"`
	UIPID          int     `json:"ui_pid,omitempty"` // dashboard we spawned via --ui (stop kills it)
}

// optimizerListenerPID returns the pid listening on addr IF it is a forge
// optimizer (verified via /healthz), else 0 — so `stop` can kill the proxy by
// port even when no pid was recorded, without ever killing an unrelated service.
func optimizerListenerPID(addr string) int {
	if addr == "" {
		return 0
	}
	if _, ours := probeExistingOptimizer(addr); !ours {
		return 0
	}
	port := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		port = addr[i+1:]
	}
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-t").Output() //nolint:gosec // fixed args
	if err != nil {
		return 0
	}
	for _, line := range strings.Fields(string(out)) {
		if pid, e := strconv.Atoi(line); e == nil {
			return pid
		}
	}
	return 0
}

// resolveChildUpstream picks the upstream base URL to hand the detached proxy
// child. It applies the shared optimizer-upstream precedence (managed settings >
// --upstream > $FORGE_OPTIMIZER_UPSTREAM > ~/.forge/settings.json), with a
// chaining fallback of the Claude Code settings gateway, else ANTHROPIC_BASE_URL
// inherited from our env — the latter is what the child would chain to on its
// own, so folding it in here means the PARENT resolves (and validates) the SAME
// effective upstream the child will use. Without it, an invalid env value would
// surface only downstream as "daemon did not come up" instead of a clean `start`
// error. A self-pointing chaining source is dropped (see resolveOptimizerUpstream).
func resolveChildUpstream(flag, settingsBase, listen string) string {
	chaining := settingsBase
	if chaining == "" || upstreamHost(chaining) == listen {
		chaining = os.Getenv("ANTHROPIC_BASE_URL")
	}
	return resolveOptimizerUpstream(flag, chaining, listen)
}

// processAlive, terminatePID, and detachSysProcAttr are platform-specific
// (POSIX signals vs Windows process handles) — see optimizer_daemon_unix.go /
// optimizer_daemon_windows.go.

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
		// Record the adopted proxy's pid so `stop` can kill it later.
		st.PID = optimizerListenerPID(listen)
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

		// Resolve the upstream the proxy forwards to and pass it EXPLICITLY to the
		// detached child. The child could resolve flags/env itself, but a gateway
		// configured only in Claude Code's settings.json (not the shell env) is
		// invisible to it — so we resolve here (including that settings gateway,
		// read BEFORE wireClaudeSettings overwrites it) and hand over one value.
		childUpstream := resolveChildUpstream(optimizerUpstream, settingsBaseURL(), listen)
		if err := validateUpstream(childUpstream, listen); err != nil {
			return fmt.Errorf("optimizer upstream: %w", err)
		}

		args := []string{"optimizer", "--compress", "--memory", "--listen", listen, "--quiet"}
		if childUpstream != "" {
			args = append(args, "--upstream", childUpstream)
			logger.Info("optimizer: forwarding to upstream", "upstream", childUpstream)
		}

		// Detached child running the standalone proxy with compression + memory.
		child := exec.Command(self, args...)    //nolint:gosec // self-exec, controlled args
		child.SysProcAttr = detachSysProcAttr() // survive this shell
		child.Stdin = nil
		child.Stdout = logf
		child.Stderr = logf
		if err := child.Start(); err != nil {
			return fmt.Errorf("starting optimizer daemon: %w", err)
		}
		st.PID = child.Process.Pid
		st.Spawned = true
		st.Upstream = childUpstream
		_ = child.Process.Release()

		if err := waitListenReady(listen, 8*time.Second); err != nil {
			return fmt.Errorf("optimizer daemon did not come up on %s (see %s): %w", listen, logPath, err)
		}
		fmt.Printf("forge optimizer started (pid %d) at http://%s → logs %s\n", st.PID, listen, logPath)
		if childUpstream != "" {
			fmt.Printf("  forwarding to upstream: %s\n", childUpstream)
		} else {
			fmt.Printf("  upstream: default (Anthropic) or $ANTHROPIC_BASE_URL — pass --upstream / $FORGE_OPTIMIZER_UPSTREAM to target a gateway\n")
		}
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

	if optimizerStartUI {
		if self, e := os.Executable(); e == nil {
			if base, uipid := ensureForgeUI(self, logger); base != "" {
				st.UIPID = uipid // 0 if we adopted an existing dashboard
				url := base + "/#/optimizer"
				fmt.Printf("opening dashboard → %s\n", url)
				openURL(url)
			}
		}
	}

	if err := writeOptDaemonState(st); err != nil {
		logger.Warn("could not persist daemon state (stop may not fully revert)", "error", err.Error())
	}

	fmt.Println("Run `claude` in any terminal — it will use the optimizer. `forge optimizer stop` to undo.")
	return nil
}

// ensureForgeUI returns (base URL, pid) of a running forge dashboard, launching
// a detached one on :4200 if none is up. pid is 0 when it adopted an existing
// dashboard (so `stop` won't kill a UI it didn't start).
func ensureForgeUI(self string, logger *slog.Logger) (string, int) {
	const base = "http://127.0.0.1:4200"
	client := &http.Client{Timeout: 600 * time.Millisecond}
	if resp, err := client.Get(base + "/api/health"); err == nil {
		_ = resp.Body.Close()
		return base, 0 // already running — adopt, don't own
	}
	logPath := forgeFile("optimizer-ui.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logger.Warn("could not open UI log", "error", err.Error())
		return "", 0
	}
	child := exec.Command(self, "ui", "--port", "4200", "--no-open") //nolint:gosec // self-exec, fixed args
	child.SysProcAttr = detachSysProcAttr()
	child.Stdin = nil
	child.Stdout = logf
	child.Stderr = logf
	if err := child.Start(); err != nil {
		_ = logf.Close()
		logger.Warn("could not start forge ui", "error", err.Error())
		return "", 0
	}
	pid := child.Process.Pid
	_ = child.Process.Release()
	// Wait briefly for readiness.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := client.Get(base + "/api/health"); err == nil {
			_ = resp.Body.Close()
			return base, pid
		}
		time.Sleep(150 * time.Millisecond)
	}
	logger.Warn("forge ui did not become ready", "log", logPath)
	return base, pid // best-effort: still try to open it
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
		// No state file — still do a best-effort full stop: revert settings,
		// deregister MCP, and kill whatever forge-optimizer is on the default addr.
		fmt.Println("no daemon state found; best-effort stop")
		_ = restoreClaudeSettings(nil, nil)
		deregisterOptimizerMCP(logger)
		if pid := optimizerListenerPID(resolveListen()); pid > 0 {
			_ = terminatePID(pid)
			fmt.Printf("stopped optimizer proxy on %s (pid %d)\n", resolveListen(), pid)
		}
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

	// Stop the daemon proxy. "stop" means stop it — prefer the recorded pid, else
	// find whatever forge-optimizer is listening on the address (handles adopted
	// proxies where no pid was recorded, the old "left it up" case).
	stopped := false
	if processAlive(st.PID) {
		if terminatePID(st.PID) == nil {
			fmt.Printf("stopped optimizer daemon (pid %d)\n", st.PID)
			stopped = true
		}
	}
	if !stopped {
		if pid := optimizerListenerPID(st.Listen); pid > 0 {
			_ = terminatePID(pid)
			fmt.Printf("stopped optimizer proxy on %s (pid %d)\n", st.Listen, pid)
			stopped = true
		}
	}
	if !stopped {
		fmt.Println("no running optimizer proxy found on " + st.Listen)
	}

	// Stop the dashboard we started via --ui (UIPID is 0 if we adopted one).
	if processAlive(st.UIPID) {
		_ = terminatePID(st.UIPID)
		fmt.Printf("stopped dashboard (pid %d)\n", st.UIPID)
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
		alive := processAlive(st.PID)
		fmt.Printf("  daemon pid  : %d (%s)\n", st.PID, map[bool]string{true: "alive", false: "not found"}[alive])
	}
	if st.Upstream != "" {
		fmt.Printf("  upstream    : %s\n", st.Upstream)
	} else if st.Spawned {
		fmt.Printf("  upstream    : default (Anthropic) / $ANTHROPIC_BASE_URL\n")
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	// Atomic write (temp in the same dir + rename) at 0600: the file can hold an
	// upstream URL with embedded credentials, and `claude` may read it
	// concurrently — a partial file would break it. 0600 keeps it owner-only.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
