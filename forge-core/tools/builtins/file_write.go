package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

type fileWriteTool struct {
	pathValidator *PathValidator
}

func (t *fileWriteTool) Name() string { return "file_write" }
func (t *fileWriteTool) Description() string {
	return "Create or overwrite a file in the project directory. Creates intermediate directories as needed. Use file_edit for modifying existing files instead of overwriting them entirely."
}
func (t *fileWriteTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *fileWriteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File path (relative to project root or absolute within project)"
			},
			"content": {
				"type": "string",
				"description": "The full file content to write"
			}
		},
		"required": ["path", "content"]
	}`)
}

func (t *fileWriteTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if strings.TrimSpace(input.Path) == "" {
		return "", fmt.Errorf("path is required")
	}

	resolved, err := t.pathValidator.Resolve(input.Path)
	if err != nil {
		return "", err
	}

	// Determine if creating or updating.
	action := "created"
	if _, statErr := os.Stat(resolved); statErr == nil {
		action = "updated"
	}

	// Create intermediate directories.
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directories: %w", err)
	}

	if err := os.WriteFile(resolved, []byte(input.Content), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	result, _ := json.Marshal(map[string]any{
		"path":   input.Path,
		"action": action,
		"bytes":  len(input.Content),
	})
	return string(result), nil
}
