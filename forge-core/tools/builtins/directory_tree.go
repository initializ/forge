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

type directoryTreeTool struct {
	pathValidator *PathValidator
}

func (t *directoryTreeTool) Name() string { return "directory_tree" }
func (t *directoryTreeTool) Description() string {
	return "Display a tree-formatted directory listing showing the structure of files and directories. Useful for understanding project layout."
}
func (t *directoryTreeTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *directoryTreeTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Directory path (relative to project root). Default: project root"
			},
			"max_depth": {
				"type": "integer",
				"description": "Maximum depth to traverse. Default: 3"
			}
		}
	}`)
}

func (t *directoryTreeTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Path     string `json:"path"`
		MaxDepth int    `json:"max_depth"`
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
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", input.Path)
	}

	maxDepth := input.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}

	var sb strings.Builder
	relRoot, _ := filepath.Rel(t.pathValidator.WorkDir(), resolved)
	if relRoot == "." {
		relRoot = filepath.Base(resolved)
	}
	sb.WriteString(relRoot + "/\n")

	t.buildTree(&sb, resolved, "", 0, maxDepth)

	return TruncateOutput(sb.String()), nil
}

func (t *directoryTreeTool) buildTree(sb *strings.Builder, dir, prefix string, depth, maxDepth int) {
	if depth >= maxDepth {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Filter out skipped directories.
	var visible []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() && skipDirs[entry.Name()] {
			continue
		}
		// Skip hidden files/dirs (starting with .).
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		visible = append(visible, entry)
	}

	for i, entry := range visible {
		isLast := i == len(visible)-1
		connector := "├── "
		childPrefix := prefix + "│   "
		if isLast {
			connector = "└── "
			childPrefix = prefix + "    "
		}

		if entry.IsDir() {
			fmt.Fprintf(sb, "%s%s%s/\n", prefix, connector, entry.Name())
			t.buildTree(sb, filepath.Join(dir, entry.Name()), childPrefix, depth+1, maxDepth)
		} else {
			sizeStr := ""
			if info, infoErr := entry.Info(); infoErr == nil && info != nil {
				bytes := info.Size()
				if bytes >= 1024*1024 {
					sizeStr = fmt.Sprintf(" (%dMB)", bytes/(1024*1024))
				} else if bytes >= 1024 {
					sizeStr = fmt.Sprintf(" (%dKB)", bytes/1024)
				}
			}
			fmt.Fprintf(sb, "%s%s%s%s\n", prefix, connector, entry.Name(), sizeStr)
		}
	}
}
