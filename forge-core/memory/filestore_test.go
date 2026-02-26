package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewFileStore(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")

	fs, err := NewFileStore(memDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Directory should be created.
	if _, err := os.Stat(memDir); err != nil {
		t.Fatalf("memory dir not created: %v", err)
	}
	if fs.Dir() != memDir {
		t.Errorf("Dir() = %q, want %q", fs.Dir(), memDir)
	}
}

func TestFileStore_ReadFile(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Write a test file.
	content := "hello world"
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := fs.ReadFile("test.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got != content {
		t.Errorf("ReadFile = %q, want %q", got, content)
	}
}

func TestFileStore_ReadFile_TraversalPrevention(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	cases := []string{
		"../etc/passwd",
		"/etc/passwd",
		"../../secret",
	}

	for _, path := range cases {
		_, err := fs.ReadFile(path)
		if err == nil {
			t.Errorf("expected error for path %q", path)
		}
	}
}

func TestFileStore_AppendDaily(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	if err := fs.AppendDaily("test observation"); err != nil {
		t.Fatalf("AppendDaily: %v", err)
	}

	// Check that a daily log was created.
	files, err := fs.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected at least one file after AppendDaily")
	}

	// Read the file and check content.
	content, err := fs.ReadFile(files[0])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(content, "test observation") {
		t.Errorf("daily log should contain observation, got: %s", content)
	}
}

func TestFileStore_EnsureMemoryMD(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	if err := fs.EnsureMemoryMD(); err != nil {
		t.Fatalf("EnsureMemoryMD: %v", err)
	}

	content, err := fs.ReadFile("MEMORY.md")
	if err != nil {
		t.Fatalf("ReadFile MEMORY.md: %v", err)
	}
	if !strings.Contains(content, "Agent Memory") {
		t.Error("MEMORY.md should contain template content")
	}

	// Second call should be a no-op.
	if err := fs.EnsureMemoryMD(); err != nil {
		t.Fatalf("EnsureMemoryMD (idempotent): %v", err)
	}
}

func TestFileStore_ListFiles(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Create test files.
	for _, name := range []string{"MEMORY.md", "2026-01-01.md", "2026-01-02.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Non-.md file should be excluded.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := fs.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(files), files)
	}
}
