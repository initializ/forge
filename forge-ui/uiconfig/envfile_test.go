package uiconfig

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetEnvFileValue_CreatesFileAndGitignore(t *testing.T) {
	workspace := t.TempDir()

	if err := SetEnvFileValue(workspace, "OPENAI_API_KEY", "sk-test"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	envPath := filepath.Join(workspace, ".forge", ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if !strings.Contains(string(raw), "OPENAI_API_KEY=sk-test") {
		t.Errorf("env file missing key:\n%s", raw)
	}

	// Permissions: 0600 (readable only by owner).
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("env file perm = %o, want 0600", mode)
	}

	// .gitignore auto-created next to .env.
	giPath := filepath.Join(workspace, ".forge", ".gitignore")
	gi, err := os.ReadFile(giPath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if !strings.Contains(string(gi), ".env") {
		t.Errorf(".gitignore should contain .env, got:\n%s", gi)
	}
}

func TestSetEnvFileValue_UpdatesInPlace(t *testing.T) {
	workspace := t.TempDir()

	for _, val := range []string{"sk-first", "sk-second", "sk-third"} {
		if err := SetEnvFileValue(workspace, "OPENAI_API_KEY", val); err != nil {
			t.Fatalf("Set %q: %v", val, err)
		}
	}

	raw, _ := os.ReadFile(filepath.Join(workspace, ".forge", ".env"))
	// Only one line per key, not three appended copies.
	count := strings.Count(string(raw), "OPENAI_API_KEY=")
	if count != 1 {
		t.Errorf("expected one OPENAI_API_KEY line, got %d:\n%s", count, raw)
	}
	if !strings.Contains(string(raw), "OPENAI_API_KEY=sk-third") {
		t.Errorf("expected latest value, got:\n%s", raw)
	}
}

func TestSetEnvFileValue_EmptyValueRemovesKey(t *testing.T) {
	workspace := t.TempDir()

	_ = SetEnvFileValue(workspace, "OPENAI_API_KEY", "sk-x")
	_ = SetEnvFileValue(workspace, "OTHER_KEY", "stays")
	if err := SetEnvFileValue(workspace, "OPENAI_API_KEY", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(workspace, ".forge", ".env"))
	if strings.Contains(string(raw), "OPENAI_API_KEY") {
		t.Errorf("cleared key should be gone:\n%s", raw)
	}
	if !strings.Contains(string(raw), "OTHER_KEY=stays") {
		t.Errorf("unrelated key should remain:\n%s", raw)
	}
}

func TestSetEnvFileValue_PreservesOtherKeysAndComments(t *testing.T) {
	workspace := t.TempDir()

	// Pre-populate the .env file with comments + unrelated keys.
	dir := filepath.Join(workspace, ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initial := "# header comment\nOTHER_KEY=value1\n# inline comment\nANOTHER=value2\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetEnvFileValue(workspace, "OPENAI_API_KEY", "sk-new"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".env"))
	got := string(raw)
	for _, want := range []string{
		"# header comment",
		"OTHER_KEY=value1",
		"# inline comment",
		"ANOTHER=value2",
		"OPENAI_API_KEY=sk-new",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in env file:\n%s", want, got)
		}
	}
}

func TestEnvLookupForWorkspace_FilePrecedesOSEnv(t *testing.T) {
	workspace := t.TempDir()
	_ = SetEnvFileValue(workspace, "OPENAI_API_KEY", "sk-from-file")

	// Plant a colliding value in OS env. The file should win for the
	// keys it defines.
	t.Setenv("OPENAI_API_KEY", "sk-from-os")
	t.Setenv("UNRELATED", "stays-os-only")

	lookup := EnvLookupForWorkspace(workspace)

	if got := lookup("OPENAI_API_KEY"); got != "sk-from-file" {
		t.Errorf("file should win for defined key, got %q", got)
	}
	if got := lookup("UNRELATED"); got != "stays-os-only" {
		t.Errorf("OS env should win for undefined-in-file key, got %q", got)
	}
}

func TestEnvLookupForWorkspace_MissingFileFallsBackToOS(t *testing.T) {
	workspace := t.TempDir() // no .forge/.env

	t.Setenv("OPENAI_API_KEY", "sk-from-os")

	lookup := EnvLookupForWorkspace(workspace)
	if got := lookup("OPENAI_API_KEY"); got != "sk-from-os" {
		t.Errorf("missing file should pass through to OS env, got %q", got)
	}
}

// Pin that the gitignore append doesn't duplicate the .env line when
// SetEnvFileValue is called multiple times.
func TestSetEnvFileValue_GitignoreIdempotent(t *testing.T) {
	workspace := t.TempDir()
	_ = SetEnvFileValue(workspace, "K1", "v1")
	_ = SetEnvFileValue(workspace, "K2", "v2")
	_ = SetEnvFileValue(workspace, "K3", "v3")

	gi, _ := os.ReadFile(filepath.Join(workspace, ".forge", ".gitignore"))
	if c := strings.Count(string(gi), ".env"); c != 1 {
		t.Errorf("expected one .env line in .gitignore, got %d:\n%s", c, gi)
	}
}

func TestSetEnvFileValue_PreservesExistingGitignoreLines(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".forge")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignore-this-too\n"), 0o644)

	if err := SetEnvFileValue(workspace, "K", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(gi), "ignore-this-too") {
		t.Errorf("operator's existing gitignore line was lost:\n%s", gi)
	}
	if !strings.Contains(string(gi), ".env") {
		t.Errorf("our .env line should be appended:\n%s", gi)
	}
}

// Sanity: SortedEnvFileKeys reflects what was written.
func TestSortedEnvFileKeys(t *testing.T) {
	workspace := t.TempDir()
	_ = SetEnvFileValue(workspace, "BBB", "1")
	_ = SetEnvFileValue(workspace, "AAA", "2")

	keys := SortedEnvFileKeys(workspace)
	if len(keys) != 2 || keys[0] != "AAA" || keys[1] != "BBB" {
		t.Errorf("SortedEnvFileKeys = %v, want [AAA BBB]", keys)
	}
}

// Sanity: confirm we can interoperate with the standard fs.FileMode
// constant rather than literal octals (catches a silly typo).
func TestSetEnvFileValue_PermConstSanity(t *testing.T) {
	workspace := t.TempDir()
	_ = SetEnvFileValue(workspace, "K", "v")
	info, _ := os.Stat(filepath.Join(workspace, ".forge", ".env"))
	if info.Mode()&fs.ModePerm != 0o600 {
		t.Errorf("perm = %v, want 0600", info.Mode()&fs.ModePerm)
	}
}
