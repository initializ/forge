package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// CustomTool wraps a discovered script as a Tool implementation.
// It delegates execution to an injected CommandExecutor rather than
// calling os/exec directly, keeping this package free of OS dependencies.
type CustomTool struct {
	name       string
	language   string
	entrypoint string
	executor   CommandExecutor
}

// NewCustomTool creates a tool wrapper for a discovered script.
// If executor is nil, Execute will return an error.
func NewCustomTool(dt DiscoveredTool, executor CommandExecutor) *CustomTool {
	return &CustomTool{
		name:       dt.Name,
		language:   dt.Language,
		entrypoint: dt.Entrypoint,
		executor:   executor,
	}
}

func (t *CustomTool) Name() string            { return t.name }
func (t *CustomTool) Description() string     { return fmt.Sprintf("Custom %s tool: %s", t.language, t.name) }
func (t *CustomTool) Category() Category      { return CategoryCustom }

func (t *CustomTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": true}`)
}

func (t *CustomTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.executor == nil {
		return "", fmt.Errorf("tool %q: no command executor configured", t.name)
	}

	runtime, runtimeArgs := t.runtimeCommand()
	cmdArgs := append(runtimeArgs, t.entrypoint)

	return t.executor.Run(ctx, runtime, cmdArgs, []byte(args))
}

func (t *CustomTool) runtimeCommand() (string, []string) {
	switch t.language {
	case "python":
		return "python3", nil
	case "typescript":
		return "npx", []string{"ts-node"}
	case "javascript":
		return "node", nil
	default:
		return t.entrypoint, nil
	}
}
