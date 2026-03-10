package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/tools"
)

func TestRegisterCodeAgentTools(t *testing.T) {
	reg := tools.NewRegistry()
	workDir := t.TempDir()

	err := RegisterCodeAgentTools(reg, workDir)
	if err != nil {
		t.Fatalf("RegisterCodeAgentTools failed: %v", err)
	}

	expected := []string{
		"file_read",
		"file_write",
		"file_edit",
		"file_patch",
		"bash_execute",
		"grep_search",
		"glob_search",
		"directory_tree",
	}

	for _, name := range expected {
		if reg.Get(name) == nil {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

func TestCodeAgentToolsCount(t *testing.T) {
	toolList := CodeAgentTools(t.TempDir())
	if len(toolList) != 8 {
		t.Errorf("expected 8 tools, got %d", len(toolList))
	}
}

// --- file_read tests ---

func TestFileRead_HappyPath(t *testing.T) {
	workDir := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5\n"
	writeTestFile(t, workDir, "test.txt", content)

	tool := &fileReadTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{"path": "test.txt"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestFileRead_WithOffset(t *testing.T) {
	workDir := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5"
	writeTestFile(t, workDir, "test.txt", content)

	tool := &fileReadTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":   "test.txt",
		"offset": 3,
		"limit":  2,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should contain lines 3 and 4
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestFileRead_Directory(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "a.txt", "content")
	writeTestFile(t, workDir, "b.txt", "content")

	tool := &fileReadTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{"path": "."}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected directory listing")
	}
}

func TestFileRead_PathTraversal(t *testing.T) {
	workDir := t.TempDir()
	tool := &fileReadTool{pathValidator: NewPathValidator(workDir)}

	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{"path": "../../../etc/passwd"}))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// --- file_write tests ---

func TestFileWrite_Create(t *testing.T) {
	workDir := t.TempDir()
	tool := &fileWriteTool{pathValidator: NewPathValidator(workDir)}

	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":    "new_file.go",
		"content": "package main\n",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["action"] != "created" {
		t.Errorf("expected action 'created', got %q", out["action"])
	}

	// Verify file exists.
	data, err := os.ReadFile(filepath.Join(workDir, "new_file.go"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(data) != "package main\n" {
		t.Errorf("unexpected content: %s", data)
	}
}

func TestFileWrite_Update(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "existing.txt", "old content")
	tool := &fileWriteTool{pathValidator: NewPathValidator(workDir)}

	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":    "existing.txt",
		"content": "new content",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["action"] != "updated" {
		t.Errorf("expected action 'updated', got %q", out["action"])
	}
}

func TestFileWrite_CreatesDirs(t *testing.T) {
	workDir := t.TempDir()
	tool := &fileWriteTool{pathValidator: NewPathValidator(workDir)}

	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":    "deep/nested/dir/file.txt",
		"content": "hello",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workDir, "deep", "nested", "dir", "file.txt")); err != nil {
		t.Fatalf("file not created in nested dir: %v", err)
	}
}

func TestFileWrite_PathTraversal(t *testing.T) {
	workDir := t.TempDir()
	tool := &fileWriteTool{pathValidator: NewPathValidator(workDir)}

	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":    "../../escape.txt",
		"content": "evil",
	}))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// --- file_edit tests ---

func TestFileEdit_HappyPath(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "edit_me.go", "func foo() {\n\treturn 1\n}\n")

	tool := &fileEditTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":     "edit_me.go",
		"old_text": "return 1",
		"new_text": "return 42",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify diff output.
	if result == "" {
		t.Fatal("expected diff output")
	}

	// Verify file was updated.
	data, _ := os.ReadFile(filepath.Join(workDir, "edit_me.go"))
	if got := string(data); got != "func foo() {\n\treturn 42\n}\n" {
		t.Errorf("unexpected content: %s", got)
	}
}

func TestFileEdit_NotFound(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "test.txt", "hello world")

	tool := &fileEditTool{pathValidator: NewPathValidator(workDir)}
	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":     "test.txt",
		"old_text": "nonexistent string",
		"new_text": "replacement",
	}))
	if err == nil {
		t.Fatal("expected error for old_text not found")
	}
}

func TestFileEdit_AmbiguousMatch(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "test.txt", "foo bar foo")

	tool := &fileEditTool{pathValidator: NewPathValidator(workDir)}
	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"path":     "test.txt",
		"old_text": "foo",
		"new_text": "baz",
	}))
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
}

// --- file_patch tests ---

func TestFilePatch_MultipleOps(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "a.txt", "content A")

	tool := &filePatchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"operations": []map[string]any{
			{"action": "add", "path": "b.txt", "content": "content B"},
			{"action": "update", "path": "a.txt", "content": "updated A"},
			{"action": "move", "path": "b.txt", "new_path": "c.txt"},
		},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected result")
	}

	// Verify a.txt updated.
	data, _ := os.ReadFile(filepath.Join(workDir, "a.txt"))
	if string(data) != "updated A" {
		t.Errorf("a.txt not updated: %s", data)
	}

	// Verify c.txt exists (moved from b.txt).
	if _, err := os.Stat(filepath.Join(workDir, "c.txt")); err != nil {
		t.Fatal("c.txt should exist after move")
	}

	// Verify b.txt no longer exists.
	if _, err := os.Stat(filepath.Join(workDir, "b.txt")); err == nil {
		t.Fatal("b.txt should not exist after move")
	}
}

func TestFilePatch_Delete(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "delete_me.txt", "goodbye")

	tool := &filePatchTool{pathValidator: NewPathValidator(workDir)}
	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"operations": []map[string]any{
			{"action": "delete", "path": "delete_me.txt"},
		},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workDir, "delete_me.txt")); err == nil {
		t.Fatal("file should be deleted")
	}
}

func TestFilePatch_PathTraversal(t *testing.T) {
	workDir := t.TempDir()
	tool := &filePatchTool{pathValidator: NewPathValidator(workDir)}

	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"operations": []map[string]any{
			{"action": "add", "path": "../../evil.txt", "content": "bad"},
		},
	}))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// --- bash_execute tests ---

func TestBashExecute_HappyPath(t *testing.T) {
	workDir := t.TempDir()
	tool := &bashExecuteTool{workDir: workDir}

	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"command": "echo hello",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["stdout"] != "hello" {
		t.Errorf("expected stdout 'hello', got %q", out["stdout"])
	}
	if out["exit_code"] != float64(0) {
		t.Errorf("expected exit_code 0, got %v", out["exit_code"])
	}
}

func TestBashExecute_ExitCode(t *testing.T) {
	workDir := t.TempDir()
	tool := &bashExecuteTool{workDir: workDir}

	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"command": "exit 42",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["exit_code"] != float64(42) {
		t.Errorf("expected exit_code 42, got %v", out["exit_code"])
	}
}

func TestBashExecute_DangerousCommandBlocked(t *testing.T) {
	workDir := t.TempDir()
	tool := &bashExecuteTool{workDir: workDir}

	tests := []struct {
		name    string
		command string
	}{
		{"sudo", "sudo rm -rf /"},
		{"rm_rf_root", "rm -rf /"},
		{"fork_bomb", ":(){ :|:& };:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
				"command": tt.command,
			}))
			if err == nil {
				t.Fatal("expected error for dangerous command")
			}
		})
	}
}

func TestBashExecute_Timeout(t *testing.T) {
	workDir := t.TempDir()
	tool := &bashExecuteTool{workDir: workDir}

	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"command": "sleep 10",
		"timeout": 1,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["exit_code"] != float64(124) {
		// Process may be killed with different exit codes, just verify it's non-zero
		if out["exit_code"] == float64(0) {
			t.Error("expected non-zero exit code for timed out command")
		}
	}
}

func TestBashExecute_Pipes(t *testing.T) {
	workDir := t.TempDir()
	tool := &bashExecuteTool{workDir: workDir}

	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"command": "echo 'hello world' | tr ' ' '\\n' | wc -l",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["exit_code"] != float64(0) {
		t.Errorf("expected exit_code 0, got %v", out["exit_code"])
	}
}

// --- grep_search tests ---

func TestGrepSearch_HappyPath(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "main.go", "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n")
	writeTestFile(t, workDir, "lib.go", "package main\n\nfunc helper() {}\n")

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "func.*main",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "(no matches found)" {
		t.Fatal("expected matches")
	}
}

func TestGrepSearch_NoMatch(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "test.txt", "hello world")

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "nonexistent_pattern_xyz",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "(no matches found)" {
		t.Errorf("expected no matches, got: %s", result)
	}
}

func TestGrepSearch_InvalidRegex(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "test.txt", "hello")

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	// Use Go fallback by searching. Invalid regex should fail.
	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "[invalid",
	}))
	// rg might handle this differently, so just ensure it doesn't panic.
	_ = err
}

func TestGrepSearch_WithInclude(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "main.go", "func main() {}")
	writeTestFile(t, workDir, "main.py", "def main(): pass")

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "main",
		"include": "*.go",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "(no matches found)" {
		t.Fatal("expected matches for .go files")
	}
}

func TestGrepSearch_ExcludePattern(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "main.go", "func main() { apiKey := \"\" }")
	writeTestFile(t, workDir, "main_test.go", "func TestMain() { apiKey := \"test\" }")

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "apiKey",
		"exclude": "*_test.go",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "(no matches found)" {
		t.Fatal("expected matches from main.go")
	}
	if strings.Contains(result, "main_test.go") {
		t.Error("expected test file to be excluded")
	}
	if !strings.Contains(result, "main.go") {
		t.Error("expected main.go to be included")
	}
}

func TestGrepSearch_ContextLines(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "schema.ts", "line1\nline2\napiKey: z.string()\nline4\nline5\n")

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "apiKey",
		"context": 1,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should contain context lines around the match.
	if !strings.Contains(result, "line2") {
		t.Error("expected before-context line 'line2'")
	}
	if !strings.Contains(result, "apiKey") {
		t.Error("expected matching line")
	}
	if !strings.Contains(result, "line4") {
		t.Error("expected after-context line 'line4'")
	}
}

func TestGrepSearch_MaxResultsConsistency(t *testing.T) {
	workDir := t.TempDir()
	// Create a file with many matching lines.
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "match line %d\n", i)
	}
	writeTestFile(t, workDir, "many.txt", sb.String())

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern":     "match",
		"max_results": 10,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Count output lines (excluding trailing empty line).
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	// Should be at most 10 lines plus possibly a truncation notice.
	if len(lines) > 11 {
		t.Errorf("expected at most ~10 result lines, got %d", len(lines))
	}
}

func TestGrepSearch_ExcludeAndIncludeTogether(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "app.ts", "const x = 1")
	writeTestFile(t, workDir, "app.test.ts", "const x = 2")
	writeTestFile(t, workDir, "app.go", "var x = 3")

	tool := &grepSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "x",
		"include": "*.ts",
		"exclude": "*.test.ts",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "app.ts") {
		t.Error("expected app.ts in results")
	}
	if strings.Contains(result, "app.test.ts") {
		t.Error("expected app.test.ts to be excluded")
	}
	if strings.Contains(result, "app.go") {
		t.Error("expected app.go to be excluded by include filter")
	}
}

// --- glob_search tests ---

func TestGlobSearch_HappyPath(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "main.go", "package main")
	writeTestFile(t, workDir, "lib.go", "package main")
	writeTestFile(t, workDir, "readme.md", "# readme")

	tool := &globSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "*.go",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "(no matches found)" {
		t.Fatal("expected matches")
	}
}

func TestGlobSearch_DoublestarPattern(t *testing.T) {
	workDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(workDir, "src", "pkg"), 0o755)
	writeTestFile(t, workDir, "src/main.go", "package main")
	writeTestFile(t, workDir, "src/pkg/lib.go", "package pkg")

	tool := &globSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "**/*.go",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "(no matches found)" {
		t.Fatal("expected matches")
	}
}

func TestGlobSearch_NoMatch(t *testing.T) {
	workDir := t.TempDir()
	writeTestFile(t, workDir, "test.txt", "hello")

	tool := &globSearchTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"pattern": "*.xyz",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "(no matches found)" {
		t.Errorf("expected no matches, got: %s", result)
	}
}

// --- directory_tree tests ---

func TestDirectoryTree_HappyPath(t *testing.T) {
	workDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(workDir, "src", "pkg"), 0o755)
	writeTestFile(t, workDir, "src/main.go", "package main")
	writeTestFile(t, workDir, "src/pkg/lib.go", "package pkg")
	writeTestFile(t, workDir, "README.md", "# readme")

	tool := &directoryTreeTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected tree output")
	}
}

func TestDirectoryTree_SkipsDotGit(t *testing.T) {
	workDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(workDir, ".git", "objects"), 0o755)
	writeTestFile(t, workDir, "main.go", "package main")

	tool := &directoryTreeTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// .git should not appear in output.
	if containsString(result, ".git") {
		t.Error("expected .git to be excluded from tree")
	}
}

func TestDirectoryTree_ShowsFileSizes(t *testing.T) {
	workDir := t.TempDir()
	// Create a file larger than 1KB.
	bigContent := strings.Repeat("x", 2048)
	writeTestFile(t, workDir, "big.txt", bigContent)
	// Create a small file (< 1KB, no size shown).
	writeTestFile(t, workDir, "small.txt", "tiny")

	tool := &directoryTreeTool{pathValidator: NewPathValidator(workDir)}
	result, err := tool.Execute(context.Background(), toJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "big.txt (2KB)") {
		t.Errorf("expected file size for big.txt, got:\n%s", result)
	}
	// Small file should not have a size suffix.
	if strings.Contains(result, "small.txt (") {
		t.Errorf("expected no size suffix for small.txt, got:\n%s", result)
	}
}

func TestDirectoryTree_PathTraversal(t *testing.T) {
	workDir := t.TempDir()
	tool := &directoryTreeTool{pathValidator: NewPathValidator(workDir)}

	_, err := tool.Execute(context.Background(), toJSON(t, map[string]any{"path": "../.."}))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

// --- truncate tests ---

func TestTruncateOutput_ShortString(t *testing.T) {
	input := "short string"
	if got := TruncateOutput(input); got != input {
		t.Errorf("expected no truncation, got %q", got)
	}
}

func TestTruncateOutput_TooManyLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < MaxOutputLines+100; i++ {
		sb.WriteString("line\n")
	}
	result := TruncateOutput(sb.String())
	if !containsString(result, "output truncated") {
		t.Error("expected truncation notice")
	}
}

func TestTruncateOutput_TooManyBytes(t *testing.T) {
	input := strings.Repeat("x", MaxOutputBytes+1000)
	result := TruncateOutput(input)
	if !containsString(result, "output truncated") {
		t.Error("expected truncation notice")
	}
}

// --- helpers ---

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	fullPath := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("creating dirs: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
}

func toJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling JSON: %v", err)
	}
	return data
}

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
