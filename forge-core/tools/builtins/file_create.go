package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/tools"
)

// allowedExtensions maps file extensions to their MIME types.
var allowedExtensions = map[string]string{
	".md":   "text/markdown",
	".json": "application/json",
	".yaml": "text/yaml",
	".yml":  "text/yaml",
	".txt":  "text/plain",
	".log":  "text/plain",
	".csv":  "text/csv",
	".sh":   "text/x-shellscript",
	".xml":  "text/xml",
	".html": "text/html",
	".py":   "text/x-python",
	".ts":   "text/typescript",
}

type fileCreateTool struct{}

func (t *fileCreateTool) Name() string { return "file_create" }
func (t *fileCreateTool) Description() string {
	return "Create a downloadable file. The file is written to a temporary directory and uploaded to the user's channel (Slack/Telegram). The result includes a 'path' field with the file's location on disk, which can be used with other tools like kubectl apply -f <path>."
}
func (t *fileCreateTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *fileCreateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filename": {
				"type": "string",
				"description": "Filename with extension (e.g., patches.yaml, report.json, output.txt, script.py)"
			},
			"content": {
				"type": "string",
				"description": "The full file content as text"
			}
		},
		"required": ["filename", "content"]
	}`)
}

func (t *fileCreateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Validate filename is not empty.
	if strings.TrimSpace(input.Filename) == "" {
		return "", fmt.Errorf("filename is required")
	}

	// Reject path traversal and directory separators.
	if strings.ContainsAny(input.Filename, "/\\") {
		return "", fmt.Errorf("filename must not contain path separators")
	}
	if input.Filename == "." || input.Filename == ".." {
		return "", fmt.Errorf("invalid filename")
	}

	// Validate extension against allowlist.
	ext := strings.ToLower(filepath.Ext(input.Filename))
	mime, ok := allowedExtensions[ext]
	if !ok {
		supported := make([]string, 0, len(allowedExtensions))
		for k := range allowedExtensions {
			supported = append(supported, k)
		}
		return "", fmt.Errorf("unsupported file extension %q; supported: %s", ext, strings.Join(supported, ", "))
	}

	// Write file to the agent's .forge/files directory if available,
	// otherwise fall back to a system temp directory.
	dir := runtime.FilesDirFromContext(ctx)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "forge-files")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating temp directory: %w", err)
	}
	filePath := filepath.Join(dir, input.Filename)
	if err := os.WriteFile(filePath, []byte(input.Content), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	// Return structured JSON for the runtime to parse.
	out, err := json.Marshal(map[string]string{
		"filename":  input.Filename,
		"content":   input.Content,
		"mime_type": mime,
		"path":      filePath,
	})
	if err != nil {
		return "", fmt.Errorf("marshalling output: %w", err)
	}
	return string(out), nil
}

// MimeFromExtension returns the MIME type for a given file extension.
// Returns empty string if the extension is not in the allowlist.
func MimeFromExtension(ext string) string {
	return allowedExtensions[strings.ToLower(ext)]
}
