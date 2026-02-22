package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	coretools "github.com/initializ/forge/forge-core/tools"
)

// CLIExecuteConfig holds the configuration for the cli_execute tool.
type CLIExecuteConfig struct {
	AllowedBinaries []string
	EnvPassthrough  []string
	TimeoutSeconds  int // default 120
	MaxOutputBytes  int // default 1MB
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

	t := &CLIExecuteTool{
		config:      config,
		allowedSet:  make(map[string]bool, len(config.AllowedBinaries)),
		binaryPaths: make(map[string]string, len(config.AllowedBinaries)),
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

// Description returns a dynamic description listing available binaries.
func (t *CLIExecuteTool) Description() string {
	if len(t.available) == 0 {
		return "Execute pre-approved CLI binaries (none available)"
	}
	return fmt.Sprintf("Execute pre-approved CLI binaries: %s", strings.Join(t.available, ", "))
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

	// Security check 1: Binary allowlist
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

	// Security check 4: Timeout
	timeout := time.Duration(t.config.TimeoutSeconds) * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Security check 5: No shell — exec.CommandContext directly
	cmd := exec.CommandContext(cmdCtx, absPath, input.Args...)

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

// buildEnv constructs an isolated environment with only PATH, HOME, LANG
// and explicitly configured passthrough variables.
func (t *CLIExecuteTool) buildEnv() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + os.Getenv("LANG"),
	}
	for _, key := range t.config.EnvPassthrough {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	return env
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
	return nil
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
