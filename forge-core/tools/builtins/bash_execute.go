package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/tools"
)

const (
	bashDefaultTimeout = 120 * time.Second
	bashMaxOutputBytes = 1 * 1024 * 1024 // 1 MB
)

// dangerousCommands is a deny-list of commands/patterns that are blocked.
var dangerousCommands = []string{
	"rm -rf /",
	"rm -rf /*",
	"mkfs.",
	"dd if=",
	":(){ :|:& };:",  // fork bomb
	"> /dev/sda",     // disk overwrite
	"chmod -R 777 /", // recursive world-writable root
	"shutdown",       // system shutdown
	"reboot",         // system reboot
	"init 0",         // system halt
	"halt",           // system halt
	"poweroff",       // system poweroff
}

// blockedPrefixes are command prefixes that are always blocked.
var blockedPrefixes = []string{
	"sudo ",
	"su ",
	"su\n",
}

type bashExecuteTool struct {
	workDir  string
	proxyURL string
}

func (t *bashExecuteTool) Name() string { return "bash_execute" }
func (t *bashExecuteTool) Description() string {
	return "Execute a bash command in the project directory. Supports pipes, redirection, and shell features. Commands run with a timeout (default 120s) and output is capped at 1MB. Dangerous commands (sudo, rm -rf /, etc.) are blocked."
}
func (t *bashExecuteTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *bashExecuteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "string",
				"description": "The bash command to execute"
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds. Default: 120, Max: 600"
			}
		},
		"required": ["command"]
	}`)
}

func (t *bashExecuteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if strings.TrimSpace(input.Command) == "" {
		return "", fmt.Errorf("command is required")
	}

	// Check deny-list.
	if err := t.validateCommand(input.Command); err != nil {
		return "", err
	}

	// Determine timeout.
	timeout := bashDefaultTimeout
	if input.Timeout > 0 {
		timeout = time.Duration(min(input.Timeout, 600)) * time.Second
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", input.Command)
	cmd.Dir = t.workDir
	cmd.Env = t.buildEnv()

	stdoutWriter := newBashLimitedWriter(bashMaxOutputBytes)
	stderrWriter := newBashLimitedWriter(bashMaxOutputBytes)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if cmdCtx.Err() == context.DeadlineExceeded {
			exitCode = 124 // timeout exit code
		} else {
			exitCode = 1
		}
	}

	result := map[string]any{
		"stdout":    strings.TrimRight(stdoutWriter.String(), "\n"),
		"stderr":    strings.TrimRight(stderrWriter.String(), "\n"),
		"exit_code": exitCode,
		"truncated": stdoutWriter.overflow || stderrWriter.overflow,
	}

	if cmdCtx.Err() == context.DeadlineExceeded {
		result["error"] = "command timed out"
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}

func (t *bashExecuteTool) validateCommand(cmd string) error {
	lower := strings.ToLower(strings.TrimSpace(cmd))

	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return fmt.Errorf("command blocked: %q is not allowed", strings.TrimSpace(prefix))
		}
	}

	for _, pattern := range dangerousCommands {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return fmt.Errorf("command blocked: contains dangerous pattern %q", pattern)
		}
	}

	return nil
}

func (t *bashExecuteTool) buildEnv() []string {
	home := os.Getenv("HOME")
	if home == "" {
		home = t.workDir
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"LANG=" + os.Getenv("LANG"),
		"TERM=xterm-256color",
		"USER=" + os.Getenv("USER"),
	}

	// Pass through DISPLAY for Linux GUI apps (browser opening).
	if runtime.GOOS == "linux" {
		if display := os.Getenv("DISPLAY"); display != "" {
			env = append(env, "DISPLAY="+display)
		}
		if xauth := os.Getenv("XAUTHORITY"); xauth != "" {
			env = append(env, "XAUTHORITY="+xauth)
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

// bashLimitedWriter caps output at a byte limit.
type bashLimitedWriter struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func newBashLimitedWriter(limit int) *bashLimitedWriter {
	return &bashLimitedWriter{limit: limit}
}

func (w *bashLimitedWriter) Write(p []byte) (int, error) {
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

func (w *bashLimitedWriter) String() string {
	return w.buf.String()
}
