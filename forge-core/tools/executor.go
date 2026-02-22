package tools

import "context"

// CommandExecutor abstracts command execution for custom tools.
type CommandExecutor interface {
	Run(ctx context.Context, command string, args []string, stdin []byte) (stdout string, err error)
}
