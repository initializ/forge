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

type filePatchTool struct {
	pathValidator *PathValidator
}

func (t *filePatchTool) Name() string { return "file_patch" }
func (t *filePatchTool) Description() string {
	return "Perform batch file operations in a single call. Supports add (create), update (overwrite), delete (remove), and move (rename) actions. All paths are validated before any changes are applied."
}
func (t *filePatchTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *filePatchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"operations": {
				"type": "array",
				"description": "List of file operations to perform",
				"items": {
					"type": "object",
					"properties": {
						"action": {
							"type": "string",
							"enum": ["add", "update", "delete", "move"],
							"description": "The operation to perform"
						},
						"path": {
							"type": "string",
							"description": "File path for the operation"
						},
						"content": {
							"type": "string",
							"description": "File content (required for add and update)"
						},
						"new_path": {
							"type": "string",
							"description": "Destination path (required for move)"
						}
					},
					"required": ["action", "path"]
				}
			}
		},
		"required": ["operations"]
	}`)
}

type patchOperation struct {
	Action  string `json:"action"`
	Path    string `json:"path"`
	Content string `json:"content"`
	NewPath string `json:"new_path"`
}

func (t *filePatchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Operations []patchOperation `json:"operations"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if len(input.Operations) == 0 {
		return "", fmt.Errorf("at least one operation is required")
	}

	// Phase 1: Validate all paths upfront before applying any changes.
	type resolvedOp struct {
		op          patchOperation
		resolved    string
		newResolved string
	}
	ops := make([]resolvedOp, len(input.Operations))

	for i, op := range input.Operations {
		if strings.TrimSpace(op.Path) == "" {
			return "", fmt.Errorf("operation %d: path is required", i)
		}

		resolved, err := t.pathValidator.Resolve(op.Path)
		if err != nil {
			return "", fmt.Errorf("operation %d: %w", i, err)
		}
		ops[i] = resolvedOp{op: op, resolved: resolved}

		switch op.Action {
		case "add", "update":
			// content is expected
		case "delete":
			// no extra validation
		case "move":
			if strings.TrimSpace(op.NewPath) == "" {
				return "", fmt.Errorf("operation %d: new_path is required for move", i)
			}
			newResolved, err := t.pathValidator.Resolve(op.NewPath)
			if err != nil {
				return "", fmt.Errorf("operation %d: new_path %w", i, err)
			}
			ops[i].newResolved = newResolved
		default:
			return "", fmt.Errorf("operation %d: unknown action %q (use add, update, delete, or move)", i, op.Action)
		}
	}

	// Phase 2: Apply operations.
	var results []map[string]string
	for _, rop := range ops {
		result := map[string]string{
			"action": rop.op.Action,
			"path":   rop.op.Path,
		}

		switch rop.op.Action {
		case "add":
			dir := filepath.Dir(rop.resolved)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("creating directories for %s: %w", rop.op.Path, err)
			}
			if err := os.WriteFile(rop.resolved, []byte(rop.op.Content), 0o644); err != nil {
				return "", fmt.Errorf("creating %s: %w", rop.op.Path, err)
			}
			result["status"] = "created"

		case "update":
			if err := os.WriteFile(rop.resolved, []byte(rop.op.Content), 0o644); err != nil {
				return "", fmt.Errorf("updating %s: %w", rop.op.Path, err)
			}
			result["status"] = "updated"

		case "delete":
			if err := os.Remove(rop.resolved); err != nil {
				return "", fmt.Errorf("deleting %s: %w", rop.op.Path, err)
			}
			result["status"] = "deleted"

		case "move":
			// Create destination directory if needed.
			destDir := filepath.Dir(rop.newResolved)
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				return "", fmt.Errorf("creating directories for %s: %w", rop.op.NewPath, err)
			}
			if err := os.Rename(rop.resolved, rop.newResolved); err != nil {
				return "", fmt.Errorf("moving %s to %s: %w", rop.op.Path, rop.op.NewPath, err)
			}
			result["status"] = "moved"
			result["new_path"] = rop.op.NewPath
		}

		results = append(results, result)
	}

	out, _ := json.Marshal(map[string]any{
		"operations": results,
		"total":      len(results),
	})
	return string(out), nil
}
