package devtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/tools"
)

// LocalFileBrowserTool reads and lists files in a project directory.
type LocalFileBrowserTool struct {
	workDir string
}

type localFileBrowserInput struct {
	Path      string `json:"path"`
	Operation string `json:"operation,omitempty"`
}

// NewLocalFileBrowserTool creates a file browser tool for the given directory.
func NewLocalFileBrowserTool(workDir string) *LocalFileBrowserTool {
	return &LocalFileBrowserTool{workDir: workDir}
}

func (t *LocalFileBrowserTool) Name() string { return "local_file_browser" }
func (t *LocalFileBrowserTool) Description() string {
	return "Read and list files in the project directory"
}
func (t *LocalFileBrowserTool) Category() tools.Category { return tools.CategoryDev }

func (t *LocalFileBrowserTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Relative path within the project directory"},
			"operation": {"type": "string", "enum": ["read", "list"], "description": "Operation: read file contents or list directory (default: read)"}
		},
		"required": ["path"]
	}`)
}

func (t *LocalFileBrowserTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input localFileBrowserInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	// Resolve and validate path is within workDir
	absWorkDir, _ := filepath.Abs(t.workDir)
	targetPath := filepath.Join(absWorkDir, filepath.Clean(input.Path))
	if !strings.HasPrefix(targetPath, absWorkDir) {
		return "", fmt.Errorf("path escapes project directory")
	}

	op := input.Operation
	if op == "" {
		op = "read"
	}

	switch op {
	case "list":
		return t.listDir(targetPath)
	case "read":
		return t.readFile(targetPath)
	default:
		return "", fmt.Errorf("unknown operation: %q", op)
	}
}

func (t *LocalFileBrowserTool) listDir(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("listing directory: %w", err)
	}

	var items []map[string]any
	for _, entry := range entries {
		item := map[string]any{
			"name":   entry.Name(),
			"is_dir": entry.IsDir(),
		}
		if info, err := entry.Info(); err == nil {
			item["size"] = info.Size()
		}
		items = append(items, item)
	}

	data, _ := json.MarshalIndent(items, "", "  ")
	return string(data), nil
}

func (t *LocalFileBrowserTool) readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	// Limit to 100KB
	if len(data) > 100*1024 {
		data = data[:100*1024]
		return string(data) + "\n... (truncated at 100KB)", nil
	}

	return string(data), nil
}
