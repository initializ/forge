package devtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

func TestLocalShellTool(t *testing.T) {
	dir := t.TempDir()
	tool := NewLocalShellTool(dir)

	if tool.Name() != "local_shell" {
		t.Errorf("name: got %q", tool.Name())
	}
	if tool.Category() != tools.CategoryDev {
		t.Errorf("category: got %q", tool.Category())
	}

	args, _ := json.Marshal(map[string]string{
		"command": "echo hello",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result should contain 'hello': %q", result)
	}

	// Verify exit code
	var output map[string]any
	json.Unmarshal([]byte(result), &output) //nolint:errcheck
	if output["exit_code"] != float64(0) {
		t.Errorf("exit_code: got %v", output["exit_code"])
	}
}

func TestLocalFileBrowserTool_ReadFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("file content"), 0644) //nolint:errcheck

	tool := NewLocalFileBrowserTool(dir)

	if tool.Name() != "local_file_browser" {
		t.Errorf("name: got %q", tool.Name())
	}
	if tool.Category() != tools.CategoryDev {
		t.Errorf("category: got %q", tool.Category())
	}

	args, _ := json.Marshal(map[string]string{
		"path":      "test.txt",
		"operation": "read",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result != "file content" {
		t.Errorf("result: got %q", result)
	}
}

func TestLocalFileBrowserTool_ListDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644) //nolint:errcheck
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)                 //nolint:errcheck

	tool := NewLocalFileBrowserTool(dir)
	args, _ := json.Marshal(map[string]string{
		"path":      ".",
		"operation": "list",
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "a.txt") {
		t.Errorf("result should list files: %q", result)
	}
	if !strings.Contains(result, "subdir") {
		t.Errorf("result should list dirs: %q", result)
	}
}

func TestLocalFileBrowserTool_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	tool := NewLocalFileBrowserTool(dir)

	args, _ := json.Marshal(map[string]string{
		"path": "../../etc/passwd",
	})

	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}
