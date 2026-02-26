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

func TestMemorySearchTool(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	// Create sample content.
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("User prefers Go over Python."), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := memory.NewManager(memory.ManagerConfig{MemoryDir: memDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close() //nolint:errcheck

	ctx := context.Background()
	if err := mgr.IndexAll(ctx); err != nil {
		t.Fatal(err)
	}

	tool := NewMemorySearchTool(mgr)

	if tool.Name() != "memory_search" {
		t.Errorf("Name = %q, want memory_search", tool.Name())
	}

	// Test search.
	args, _ := json.Marshal(map[string]any{"query": "Go Python"})
	result, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "Go over Python") {
		t.Errorf("expected result to contain memory content, got: %s", result)
	}

	// Test empty query.
	args, _ = json.Marshal(map[string]any{"query": ""})
	_, err = tool.Execute(ctx, args)
	if err == nil {
		t.Error("expected error for empty query")
	}
}
