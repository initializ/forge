package builtins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/memory"
)

func TestMemoryGetTool(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	// Create sample content.
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# My Memory\nImportant facts here."
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := memory.NewManager(memory.ManagerConfig{MemoryDir: memDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close() //nolint:errcheck

	tool := NewMemoryGetTool(mgr)

	if tool.Name() != "memory_get" {
		t.Errorf("Name = %q, want memory_get", tool.Name())
	}

	// Test reading file.
	args, _ := json.Marshal(map[string]string{"path": "MEMORY.md"})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Important facts here") {
		t.Errorf("expected content, got: %s", result)
	}

	// Test empty path.
	args, _ = json.Marshal(map[string]string{"path": ""})
	_, err = tool.Execute(context.Background(), args)
	if err == nil {
		t.Error("expected error for empty path")
	}

	// Test non-existent file.
	args, _ = json.Marshal(map[string]string{"path": "nonexistent.md"})
	_, err = tool.Execute(context.Background(), args)
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}
