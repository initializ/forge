package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManager_NewManager(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	mgr, err := NewManager(ManagerConfig{MemoryDir: memDir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close() //nolint:errcheck

	// MEMORY.md should be created.
	if _, err := os.Stat(filepath.Join(memDir, "MEMORY.md")); err != nil {
		t.Error("MEMORY.md should be created on init")
	}
}

func TestManager_EmptyDir(t *testing.T) {
	_, err := NewManager(ManagerConfig{})
	if err == nil {
		t.Error("expected error for empty memory dir")
	}
}

func TestManager_GetFile(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	mgr, err := NewManager(ManagerConfig{MemoryDir: memDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close() //nolint:errcheck

	content, err := mgr.GetFile("MEMORY.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !strings.Contains(content, "Agent Memory") {
		t.Error("MEMORY.md should contain template content")
	}
}

func TestManager_AppendDailyLog(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	mgr, err := NewManager(ManagerConfig{MemoryDir: memDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close() //nolint:errcheck

	ctx := context.Background()
	if err := mgr.AppendDailyLog(ctx, "test observation"); err != nil {
		t.Fatalf("AppendDailyLog: %v", err)
	}

	// The daily log should exist and be searchable (keyword mode since no embedder).
	results, err := mgr.Search(ctx, "test observation")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Should find the observation in results.
	found := false
	for _, r := range results {
		if strings.Contains(r.Chunk.Content, "test observation") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find daily log observation in search results")
	}
}

func TestManager_IndexAll(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	// Create some test files.
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("# Memory\nUser prefers Go."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "2026-02-25.md"), []byte("## 10:00\nDiscussed architecture."), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(ManagerConfig{MemoryDir: memDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close() //nolint:errcheck

	ctx := context.Background()
	if err := mgr.IndexAll(ctx); err != nil {
		t.Fatalf("IndexAll: %v", err)
	}

	// Search should find indexed content.
	results, err := mgr.Search(ctx, "Go")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected search results after IndexAll")
	}
}

func TestManager_SearchWithMockEmbedder(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("The user prefers TypeScript over JavaScript."), 0o644); err != nil {
		t.Fatal(err)
	}

	emb := &mockEmbedder{dims: 4}
	mgr, err := NewManager(ManagerConfig{
		MemoryDir: memDir,
		Embedder:  emb,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close() //nolint:errcheck

	ctx := context.Background()
	if err := mgr.IndexAll(ctx); err != nil {
		t.Fatal(err)
	}

	results, err := mgr.Search(ctx, "TypeScript")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected search results with embedder")
	}
}

func TestManager_GracefulDegradation(t *testing.T) {
	// Verify no crash when memory dir doesn't have any files.
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	mgr, err := NewManager(ManagerConfig{MemoryDir: memDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close() //nolint:errcheck

	ctx := context.Background()

	// Search on empty index should return nil, not error.
	results, err := mgr.Search(ctx, "anything")
	if err != nil {
		t.Fatalf("Search on empty: %v", err)
	}
	// Results may be nil or empty — both are fine.
	_ = results
}
