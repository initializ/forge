package surface

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

	"github.com/initializ/forge/forge-core/tools"
)

// shellTimeout bounds a single shell command so a hung process can't wedge the
// native session.
const shellTimeout = 3 * time.Minute

// ShellTool runs shell commands for the native builder agent — the OS ops
// (ls, mkdir, grep, cat, …) and, because the running forge binary's directory
// is prepended to PATH, the full `forge` CLI. It is the local-operator
// equivalent of a coding agent's Bash tool: unsandboxed by design and confined
// by default to the surface's working directory. It is CLI-local and never
// registered for a deployed agent.
type ShellTool struct {
	base string
}

// NewShellTool builds a shell tool rooted at base.
func NewShellTool(base string) ShellTool { return ShellTool{base: base} }

// Name implements tools.Tool.
func (ShellTool) Name() string { return "shell" }

// Description implements tools.Tool.
func (ShellTool) Description() string {
	return "Run a shell command in the workspace (via `sh -c`). Use for OS operations " +
		"(ls, mkdir, grep, cat, sed) and for running the `forge` CLI directly " +
		"(forge init/validate/build/run/skills/channel/mcp). Combine with the file " +
		"tools to build and edit a Forge agent."
}

// Category implements tools.Tool.
func (ShellTool) Category() tools.Category { return tools.CategoryDev }

// InputSchema implements tools.Tool.
func (ShellTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["command"],
  "properties": {
    "command": {"type": "string", "description": "The shell command to run, e.g. \"forge validate\" or \"mkdir -p skills/foo\"."},
    "dir": {"type": "string", "description": "Subdirectory to run in (relative to the workspace; default: workspace root)."}
  }
}`)
}

// Execute implements tools.Tool.
func (s ShellTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
		Dir     string `json:"dir,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("shell: invalid arguments: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("shell: command is required")
	}

	runCtx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", in.Command)
	cmd.Dir = safeJoin(s.base, in.Dir)
	cmd.Env = shellEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()

	out := capText(strings.TrimSpace(buf.String()), maxDocChars)
	if runCtx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("shell command timed out after %s", shellTimeout)
	}
	if runErr != nil {
		if out == "" {
			return "", fmt.Errorf("shell command failed: %w", runErr)
		}
		return out, fmt.Errorf("shell command exited non-zero: %w", runErr)
	}
	if out == "" {
		out = "(command produced no output)"
	}
	return out, nil
}

// shellEnv returns the process environment with the running forge binary's
// directory prepended to PATH, so the agent's `forge …` calls hit this build.
func shellEnv() []string {
	env := os.Environ()
	bin, err := forgeExecutable()
	if err != nil {
		return env
	}
	dir := filepath.Dir(bin)
	out := make([]string, 0, len(env))
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, "PATH="+dir+string(os.PathListSeparator)+kv[len("PATH="):])
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, "PATH="+dir)
	}
	return out
}
