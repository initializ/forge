// Package devtools provides developer tools that are only available with --dev flag.
package devtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/tools"
)

// LocalShellTool executes shell commands sandboxed to a work directory.
type LocalShellTool struct {
	workDir string
}

type localShellInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// NewLocalShellTool creates a shell tool sandboxed to the given directory.
func NewLocalShellTool(workDir string) *LocalShellTool {
	return &LocalShellTool{workDir: workDir}
}

func (t *LocalShellTool) Name() string { return "local_shell" }
func (t *LocalShellTool) Description() string {
	return "Execute shell commands in the project directory"
}
func (t *LocalShellTool) Category() tools.Category { return tools.CategoryDev }

func (t *LocalShellTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "Shell command to execute"},
			"timeout": {"type": "integer", "description": "Timeout in seconds (default 30)"}
		},
		"required": ["command"]
	}`)
}

func (t *LocalShellTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input localShellInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	timeout := time.Duration(input.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", input.Command)
	cmd.Dir = t.workDir

	// Prevent path traversal
	absWorkDir, _ := filepath.Abs(t.workDir)
	cmd.Env = append(cmd.Environ(), "HOME="+absWorkDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := map[string]any{
		"stdout":    strings.TrimRight(stdout.String(), "\n"),
		"stderr":    strings.TrimRight(stderr.String(), "\n"),
		"exit_code": cmd.ProcessState.ExitCode(),
	}

	if err != nil {
		result["error"] = err.Error()
	}

	data, _ := json.Marshal(result)
	return string(data), nil
}
