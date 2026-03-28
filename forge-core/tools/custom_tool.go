package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func (t *CustomTool) Name() string { return t.name }
func (t *CustomTool) Description() string {
	return fmt.Sprintf("Custom %s tool: %s", t.language, t.name)
}
func (t *CustomTool) Category() Category { return CategoryCustom }

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

// ValidateEntrypoint checks that the entrypoint is safe to execute:
// - Not empty or absolute
// - Does not contain path traversal (..)
// - Resolves (via symlinks) to a path within basedir
// - Is a regular file
func (t *CustomTool) ValidateEntrypoint(basedir string) error {
	if t.entrypoint == "" {
		return fmt.Errorf("entrypoint is empty")
	}
	if filepath.IsAbs(t.entrypoint) {
		return fmt.Errorf("entrypoint must be a relative path, got %q", t.entrypoint)
	}
	cleaned := filepath.Clean(t.entrypoint)
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("entrypoint contains path traversal: %q", t.entrypoint)
	}

	realBase, err := filepath.EvalSymlinks(basedir)
	if err != nil {
		return fmt.Errorf("resolving basedir: %w", err)
	}
	fullPath := filepath.Join(realBase, cleaned)
	realPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return fmt.Errorf("resolving entrypoint: %w", err)
	}
	if !strings.HasPrefix(realPath, realBase+string(filepath.Separator)) && realPath != realBase {
		return fmt.Errorf("entrypoint %q resolves outside basedir", t.entrypoint)
	}

	info, err := os.Stat(realPath)
	if err != nil {
		return fmt.Errorf("stat entrypoint: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("entrypoint %q is not a regular file", t.entrypoint)
	}
	return nil
}

func (t *CustomTool) runtimeCommand() (string, []string) {
	switch t.language {
	case "python":
		return "python3", nil
	case "typescript":
		return "npx", []string{"--no-install", "ts-node"}
	case "javascript":
		return "node", nil
	default:
		return t.entrypoint, nil
	}
}
