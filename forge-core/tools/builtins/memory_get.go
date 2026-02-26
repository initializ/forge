package builtins

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/memory"
	"github.com/initializ/forge/forge-core/tools"
)

type memoryGetTool struct {
	mgr *memory.Manager
}

// NewMemoryGetTool creates a memory_get tool backed by a Manager.
// This tool is registered conditionally (not via All()) since it needs
// a Manager instance.
func NewMemoryGetTool(mgr *memory.Manager) tools.Tool {
	return &memoryGetTool{mgr: mgr}
}

type memoryGetInput struct {
	Path string `json:"path"`
}

func (t *memoryGetTool) Name() string { return "memory_get" }
func (t *memoryGetTool) Description() string {
	return "Read a specific memory file (e.g. MEMORY.md for curated facts, or a daily log like 2026-02-25.md)"
}
func (t *memoryGetTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *memoryGetTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Relative path to the memory file (e.g. MEMORY.md, 2026-02-25.md)"}
		},
		"required": ["path"]
	}`)
}

func (t *memoryGetTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input memoryGetInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	if input.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	content, err := t.mgr.GetFile(input.Path)
	if err != nil {
		return "", fmt.Errorf("reading memory file: %w", err)
	}

	return content, nil
}
