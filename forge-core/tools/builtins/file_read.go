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

type fileReadTool struct {
	pathValidator *PathValidator
}

func (t *fileReadTool) Name() string { return "file_read" }
func (t *fileReadTool) Description() string {
	return "Read a file's contents with optional line offset and limit, or list a directory's entries. Returns numbered lines (cat -n style) for files, or a listing with name, type, and size for directories."
}
func (t *fileReadTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *fileReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File or directory path (relative to project root or absolute within project)"
			},
			"offset": {
				"type": "integer",
				"description": "Line number to start reading from (1-based). Default: 1"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of lines to read. Default: 2000"
			}
		},
		"required": ["path"]
	}`)
}

func (t *fileReadTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	resolved, err := t.pathValidator.Resolve(input.Path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("cannot access %q: %w", input.Path, err)
	}

	if info.IsDir() {
		return t.listDirectory(resolved)
	}

	return t.readFile(resolved, input.Offset, input.Limit)
}

func (t *fileReadTool) readFile(path string, offset, limit int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = MaxOutputLines
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	// Convert to 0-based index.
	start := offset - 1
	if start >= totalLines {
		return fmt.Sprintf("(file has %d lines, offset %d is past end)", totalLines, offset), nil
	}

	end := min(start+limit, totalLines)

	var sb strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
	}

	result := sb.String()
	if end < totalLines {
		result += fmt.Sprintf("\n... (%d more lines not shown)", totalLines-end)
	}

	return TruncateOutput(result), nil
}

func (t *fileReadTool) listDirectory(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("reading directory: %w", err)
	}

	var sb strings.Builder
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		entryType := "file"
		if entry.IsDir() {
			entryType = "dir"
		} else if info.Mode()&os.ModeSymlink != 0 {
			entryType = "link"
		}
		relPath, _ := filepath.Rel(t.pathValidator.WorkDir(), filepath.Join(path, entry.Name()))
		fmt.Fprintf(&sb, "%-6s %10d  %s\n", entryType, info.Size(), relPath)
	}

	if sb.Len() == 0 {
		return "(empty directory)", nil
	}

	return TruncateOutput(sb.String()), nil
}
