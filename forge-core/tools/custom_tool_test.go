package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeCommand_TypeScript(t *testing.T) {
	ct := &CustomTool{language: "typescript"}
	cmd, args := ct.runtimeCommand()
	if cmd != "npx" {
		t.Fatalf("command = %q, want npx", cmd)
	}
	if len(args) < 2 || args[0] != "--no-install" || args[1] != "ts-node" {
		t.Fatalf("args = %v, want [--no-install ts-node]", args)
	}
}

func TestRuntimeCommand_Python(t *testing.T) {
	ct := &CustomTool{language: "python"}
	cmd, args := ct.runtimeCommand()
	if cmd != "python3" {
		t.Fatalf("command = %q, want python3", cmd)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want nil", args)
	}
}

func TestRuntimeCommand_JavaScript(t *testing.T) {
	ct := &CustomTool{language: "javascript"}
	cmd, args := ct.runtimeCommand()
	if cmd != "node" {
		t.Fatalf("command = %q, want node", cmd)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want nil", args)
	}
}

func TestRuntimeCommand_Default(t *testing.T) {
	ct := &CustomTool{language: "ruby", entrypoint: "script.rb"}
	cmd, args := ct.runtimeCommand()
	if cmd != "script.rb" {
		t.Fatalf("command = %q, want script.rb", cmd)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want nil", args)
	}
}

func TestValidateEntrypoint_Valid(t *testing.T) {
	basedir := t.TempDir()
	toolDir := filepath.Join(basedir, "tools")
	if err := os.MkdirAll(toolDir, 0755); err != nil {
		t.Fatal(err)
	}
	entrypoint := filepath.Join("tools", "tool_foo.py")
	fullPath := filepath.Join(basedir, entrypoint)
	if err := os.WriteFile(fullPath, []byte("# tool"), 0644); err != nil {
		t.Fatal(err)
	}

	ct := &CustomTool{entrypoint: entrypoint}
	if err := ct.ValidateEntrypoint(basedir); err != nil {
		t.Fatalf("ValidateEntrypoint() unexpected error: %v", err)
	}
}

func TestValidateEntrypoint_PathTraversal(t *testing.T) {
	basedir := t.TempDir()
	ct := &CustomTool{entrypoint: "../../etc/passwd"}
	if err := ct.ValidateEntrypoint(basedir); err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestValidateEntrypoint_AbsolutePath(t *testing.T) {
	basedir := t.TempDir()
	ct := &CustomTool{entrypoint: "/tmp/evil.py"}
	if err := ct.ValidateEntrypoint(basedir); err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestValidateEntrypoint_NotRegularFile(t *testing.T) {
	basedir := t.TempDir()
	dirPath := filepath.Join(basedir, "tools")
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		t.Fatal(err)
	}

	ct := &CustomTool{entrypoint: "tools"}
	if err := ct.ValidateEntrypoint(basedir); err == nil {
		t.Fatal("expected error for directory entrypoint")
	}
}
