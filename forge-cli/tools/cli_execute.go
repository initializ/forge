package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	coretools "github.com/initializ/forge/forge-core/tools"
)

// CLIExecuteConfig holds the configuration for the cli_execute tool.
type CLIExecuteConfig struct {
	AllowedBinaries []string
	EnvPassthrough  []string
	TimeoutSeconds  int    // default 120
	MaxOutputBytes  int    // default 1MB
	WorkDir         string // confine path arguments to this directory
}

// CLIExecuteTool is a Category-A builtin tool that executes only pre-approved
// CLI binaries via exec.Command (no shell), with env isolation, timeouts, and
// output limits.
type CLIExecuteTool struct {
	config      CLIExecuteConfig
	allowedSet  map[string]bool   // O(1) allowlist lookup
	binaryPaths map[string]string // resolved absolute paths from exec.LookPath
	available   []string
	missing     []string
	proxyURL    string // egress proxy URL (e.g., "http://127.0.0.1:54321")
	workDir     string // resolved absolute workDir for path confinement
	homeDir     string // resolved $HOME for path confinement
}

// cliExecuteArgs is the JSON input schema for Execute.
type cliExecuteArgs struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
	Stdin  string   `json:"stdin,omitempty"`
}

// cliExecuteResult is the JSON output format matching local_shell pattern.
type cliExecuteResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

// NewCLIExecuteTool creates a CLIExecuteTool from the given config.
// It resolves each binary via exec.LookPath at startup and records availability.
func NewCLIExecuteTool(config CLIExecuteConfig) *CLIExecuteTool {
	if config.TimeoutSeconds <= 0 {
		config.TimeoutSeconds = 120
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = 1048576 // 1MB
	}

	// Resolve workDir and homeDir for path confinement.
	workDir := config.WorkDir
	if workDir != "" {
		if abs, err := filepath.Abs(workDir); err == nil {
			workDir = abs
		}
	}
	homeDir := os.Getenv("HOME")

	// Filter denied shells from the allowed list before constructing the
	// tool. Execute() blocks them at runtime, but including them in the
	// schema/description causes the LLM to hallucinate they are available.
	filtered := make([]string, 0, len(config.AllowedBinaries))
	for _, bin := range config.AllowedBinaries {
		if !deniedShells[bin] {
			filtered = append(filtered, bin)
		}
	}
	config.AllowedBinaries = filtered

	t := &CLIExecuteTool{
		config:      config,
		allowedSet:  make(map[string]bool, len(config.AllowedBinaries)),
		binaryPaths: make(map[string]string, len(config.AllowedBinaries)),
		workDir:     workDir,
		homeDir:     homeDir,
	}

	for _, bin := range config.AllowedBinaries {
		t.allowedSet[bin] = true
		absPath, err := exec.LookPath(bin)
		if err != nil {
			t.missing = append(t.missing, bin)
		} else {
			t.binaryPaths[bin] = absPath
			t.available = append(t.available, bin)
		}
	}

	return t
}

// Name returns the tool name.
func (t *CLIExecuteTool) Name() string { return "cli_execute" }

// Category returns CategoryBuiltin.
func (t *CLIExecuteTool) Category() coretools.Category { return coretools.CategoryBuiltin }

// Description returns a description of the tool. Binary names are deliberately
// omitted — listing them here causes the LLM to regurgitate them when users
// ask capability questions. The LLM discovers allowed binaries from the schema enum.
func (t *CLIExecuteTool) Description() string {
	if len(t.available) == 0 {
		return "Execute CLI commands for skill operations (none available)"
	}
	return "Execute CLI commands for skill operations. Use the binary field's allowed values from the schema."
}

// InputSchema returns a dynamic JSON schema with the binary field's enum
// populated from AllowedBinaries.
func (t *CLIExecuteTool) InputSchema() json.RawMessage {
	// Build enum array for binary field
	enumItems := make([]string, 0, len(t.config.AllowedBinaries))
	for _, bin := range t.config.AllowedBinaries {
		enumItems = append(enumItems, fmt.Sprintf("%q", bin))
	}

	schema := fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "binary": {
      "type": "string",
      "description": "The binary to execute (must be from the allowed list)",
      "enum": [%s]
    },
    "args": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Command-line arguments to pass to the binary"
    },
    "stdin": {
      "type": "string",
      "description": "Optional stdin input to pipe to the process"
    }
  },
  "required": ["binary"]
}`, strings.Join(enumItems, ", "))

	return json.RawMessage(schema)
}

// Execute runs the specified binary with the given arguments after performing
// all security checks.
func (t *CLIExecuteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input cliExecuteArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("cli_execute: invalid arguments: %w", err)
	}

	// Security check 1a: Block shell interpreters — these defeat the no-shell
	// exec.Command design and bypass all path argument validation.
	if deniedShells[input.Binary] {
		return "", fmt.Errorf("cli_execute: binary %q is a shell interpreter and cannot be used", input.Binary)
	}

	// Security check 1b: Binary allowlist
	if !t.allowedSet[input.Binary] {
		return "", fmt.Errorf("cli_execute: binary %q is not in the allowed list", input.Binary)
	}

	// Security check 2: Binary availability
	absPath, ok := t.binaryPaths[input.Binary]
	if !ok {
		return "", fmt.Errorf("cli_execute: binary %q was not found on this system", input.Binary)
	}

	// Security check 3: Arg validation (defense-in-depth)
	for i, arg := range input.Args {
		if err := validateArg(arg); err != nil {
			return "", fmt.Errorf("cli_execute: argument %d: %w", i, err)
		}
	}

	// Security check 3b: Path confinement — block path args that escape workDir
	// into $HOME (e.g., ~/Library/Keychains/, ../../../.ssh/id_rsa)
	if t.workDir != "" {
		for i, arg := range input.Args {
			if err := t.validatePathArg(arg); err != nil {
				return "", fmt.Errorf("cli_execute: argument %d: %w", i, err)
			}
		}
	}

	// Security check 4: Timeout
	timeout := time.Duration(t.config.TimeoutSeconds) * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Security check 5: No shell — exec.CommandContext directly
	cmd := exec.CommandContext(cmdCtx, absPath, input.Args...)

	// Defense-in-depth: set working directory so relative paths resolve within workDir
	if t.workDir != "" {
		cmd.Dir = t.workDir
	}

	// Security check 6: Env isolation
	cmd.Env = t.buildEnv()

	// Stdin
	if input.Stdin != "" {
		cmd.Stdin = strings.NewReader(input.Stdin)
	}

	// Security check 7: Output limit
	stdoutWriter := newLimitedWriter(t.config.MaxOutputBytes)
	stderrWriter := newLimitedWriter(t.config.MaxOutputBytes)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	// Run the command
	exitCode := 0
	err := cmd.Run()
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("cli_execute: command timed out after %ds", t.config.TimeoutSeconds)
		}
		// Extract exit code from ExitError
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", fmt.Errorf("cli_execute: failed to run command: %w", err)
		}
	}

	// Build result
	result := cliExecuteResult{
		Stdout:    stdoutWriter.String(),
		Stderr:    stderrWriter.String(),
		ExitCode:  exitCode,
		Truncated: stdoutWriter.overflow || stderrWriter.overflow,
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("cli_execute: failed to marshal result: %w", err)
	}

	return string(resultJSON), nil
}

// Availability returns the lists of available and missing binaries.
func (t *CLIExecuteTool) Availability() (available, missing []string) {
	return t.available, t.missing
}

// SetProxyURL sets the egress proxy URL for subprocess env injection.
func (t *CLIExecuteTool) SetProxyURL(url string) { t.proxyURL = url }

// buildEnv constructs an isolated environment with only PATH, HOME, LANG
// and explicitly configured passthrough variables. When workDir is set,
// HOME is overridden to workDir so subprocess ~ expansion stays confined.
func (t *CLIExecuteTool) buildEnv() []string {
	homeVal := os.Getenv("HOME")
	if t.workDir != "" {
		homeVal = t.workDir
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + homeVal,
		"LANG=" + os.Getenv("LANG"),
	}
	for _, key := range t.config.EnvPassthrough {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	if t.proxyURL != "" {
		env = append(env,
			"HTTP_PROXY="+t.proxyURL,
			"HTTPS_PROXY="+t.proxyURL,
			"http_proxy="+t.proxyURL,
			"https_proxy="+t.proxyURL,
		)
	}
	return env
}

// deniedShells is a hardcoded set of shell interpreters that are never allowed
// regardless of the allowlist. Shells defeat the security model by
// reintroducing shell interpretation, bypassing path validation and the
// no-shell exec.Command design.
var deniedShells = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true,
	"ksh": true, "csh": true, "tcsh": true, "fish": true,
}

// validateArg rejects arguments containing shell injection patterns.
// Since we use exec.Command (no shell), these are defense-in-depth checks
// against confused upstream processing.
func validateArg(arg string) error {
	if strings.Contains(arg, "$(") {
		return fmt.Errorf("argument contains command substitution '$(': %q", arg)
	}
	if strings.Contains(arg, "`") {
		return fmt.Errorf("argument contains backtick: %q", arg)
	}
	if strings.ContainsAny(arg, "\n\r") {
		return fmt.Errorf("argument contains newline: %q", arg)
	}
	// Defense-in-depth: block file:// URLs which can read the host filesystem.
	if strings.Contains(strings.ToLower(arg), "file://") {
		return fmt.Errorf("argument contains file:// protocol: %q", arg)
	}
	return nil
}

// validatePathArg checks whether an argument looks like a filesystem path and,
// if so, ensures it doesn't resolve to a location inside $HOME but outside
// workDir. System paths (outside $HOME) and non-path arguments pass through.
func (t *CLIExecuteTool) validatePathArg(arg string) error {
	if !looksLikePath(arg) {
		return nil
	}
	resolved := resolveArgPath(arg, t.workDir, t.homeDir)

	// If the resolved path is inside $HOME (or is $HOME itself) but outside workDir → blocked.
	inHome := resolved == t.homeDir || strings.HasPrefix(resolved, t.homeDir+"/")
	inWorkDir := resolved == t.workDir || strings.HasPrefix(resolved, t.workDir+"/")
	if t.homeDir != "" && inHome && !inWorkDir {
		return fmt.Errorf("path %q resolves outside the agent working directory", arg)
	}
	return nil
}

// looksLikePath returns true for arguments that look like filesystem paths.
// Only bare path prefixes are matched; flag arguments (--foo=/bar) are not
// detected so that flags like --kubeconfig=~/.kube/config pass through.
func looksLikePath(arg string) bool {
	return strings.HasPrefix(arg, "/") ||
		strings.HasPrefix(arg, "~/") ||
		strings.HasPrefix(arg, "./") ||
		strings.HasPrefix(arg, "../") ||
		arg == "~" || arg == "." || arg == ".."
}

// resolveArgPath expands ~ and resolves relative paths against workDir,
// then cleans the result to eliminate .. components.
func resolveArgPath(arg, workDir, homeDir string) string {
	if strings.HasPrefix(arg, "~/") {
		arg = filepath.Join(homeDir, arg[2:])
	} else if arg == "~" {
		arg = homeDir
	} else if !filepath.IsAbs(arg) {
		arg = filepath.Join(workDir, arg)
	}
	return filepath.Clean(arg)
}

// ParseCLIExecuteConfig extracts typed config from the map[string]any that
// YAML produces. Handles both int and float64 for numeric fields.
func ParseCLIExecuteConfig(raw map[string]any) CLIExecuteConfig {
	cfg := CLIExecuteConfig{}

	if bins, ok := raw["allowed_binaries"]; ok {
		if binSlice, ok := bins.([]any); ok {
			for _, b := range binSlice {
				if s, ok := b.(string); ok {
					cfg.AllowedBinaries = append(cfg.AllowedBinaries, s)
				}
			}
		}
	}

	if envPass, ok := raw["env_passthrough"]; ok {
		if envSlice, ok := envPass.([]any); ok {
			for _, e := range envSlice {
				if s, ok := e.(string); ok {
					cfg.EnvPassthrough = append(cfg.EnvPassthrough, s)
				}
			}
		}
	}

	if timeout, ok := raw["timeout"]; ok {
		cfg.TimeoutSeconds = toInt(timeout)
	}

	if maxOutput, ok := raw["max_output_bytes"]; ok {
		cfg.MaxOutputBytes = toInt(maxOutput)
	}

	return cfg
}

// toInt converts a numeric value from YAML/JSON (may be int or float64) to int.
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}

// limitedWriter wraps a bytes.Buffer and silently drops bytes after the limit.
// It always returns len(p) to avoid broken pipe errors from subprocesses.
type limitedWriter struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func newLimitedWriter(limit int) *limitedWriter {
	return &limitedWriter{limit: limit}
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.overflow = true
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.overflow = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *limitedWriter) String() string {
	return w.buf.String()
}
