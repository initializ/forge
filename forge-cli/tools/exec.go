package tools

import (
	"bytes"
	"context"
	"fmt"
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
