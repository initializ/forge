package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

type fileEditTool struct {
	pathValidator *PathValidator
}

func (t *fileEditTool) Name() string { return "file_edit" }
func (t *fileEditTool) Description() string {
	return "Edit a file by replacing an exact string match with new text. The old_text must match exactly one location in the file. Returns a unified diff of the change. Always read a file before editing to get exact text."
}
func (t *fileEditTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *fileEditTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File path (relative to project root or absolute within project)"
			},
			"old_text": {
				"type": "string",
				"description": "The exact text to find and replace (must be unique in the file)"
			},
			"new_text": {
				"type": "string",
				"description": "The replacement text"
			}
		},
		"required": ["path", "old_text", "new_text"]
	}`)
}

func (t *fileEditTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if strings.TrimSpace(input.Path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if input.OldText == "" {
		return "", fmt.Errorf("old_text is required")
	}
	if input.OldText == input.NewText {
		return "", fmt.Errorf("old_text and new_text are identical")
	}

	resolved, err := t.pathValidator.Resolve(input.Path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	content := string(data)
	count := strings.Count(content, input.OldText)
	if count == 0 {
		return "", fmt.Errorf("old_text not found in %s", input.Path)
	}
	if count > 1 {
		return "", fmt.Errorf("old_text found %d times in %s — must be unique. Provide more surrounding context to make it unique", count, input.Path)
	}

	// Perform replacement.
	newContent := strings.Replace(content, input.OldText, input.NewText, 1)

	if err := os.WriteFile(resolved, []byte(newContent), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	// Generate a simple diff output.
	diff := generateDiff(input.Path, input.OldText, input.NewText)
	return diff, nil
}

// generateDiff creates a unified-diff-like representation of the change.
func generateDiff(path, oldText, newText string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", path, path)

	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	for _, line := range oldLines {
		fmt.Fprintf(&sb, "-%s\n", line)
	}
	for _, line := range newLines {
		fmt.Fprintf(&sb, "+%s\n", line)
	}

	return sb.String()
}
