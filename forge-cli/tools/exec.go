package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// OSCommandExecutor implements tools.CommandExecutor using os/exec.
type OSCommandExecutor struct{}

func (e *OSCommandExecutor) Run(ctx context.Context, command string, args []string, stdin []byte) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("command error: %s", stderr.String())
		}
		return "", fmt.Errorf("command execution failed: %w", err)
	}

	return stdout.String(), nil
}

// SkillCommandExecutor implements tools.CommandExecutor with a configurable
// timeout and environment variable passthrough for skill scripts.
type SkillCommandExecutor struct {
	Timeout time.Duration
	EnvVars []string // extra env var names to pass through (e.g., "TAVILY_API_KEY")
}

func (e *SkillCommandExecutor) Run(ctx context.Context, command string, args []string, stdin []byte) (string, error) {
	timeout := e.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)

	// Build minimal environment with only explicitly allowed variables.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	for _, name := range e.EnvVars {
		if val := os.Getenv(name); val != "" {
			env = append(env, name+"="+val)
		}
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("skill command error: %s", stderr.String())
		}
		return "", fmt.Errorf("skill command execution failed: %w", err)
	}

	return stdout.String(), nil
}
