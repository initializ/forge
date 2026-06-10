package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveChecksumsDir_OperatorLayoutPreferred and the companion
// TestResolveChecksumsDir_FlattenedContainerLayout pin the two paths
// the runner checks for checksums.json (issue #147). Pre-fix the
// runner only looked at <WorkDir>/.forge-output/checksums.json, which
// never exists inside a Forge container — `COPY . .` from the
// .forge-output build context places the file at /app/checksums.json
// directly. Result: build-integrity verification was silently disabled
// in every Forge container.
//
// These tests don't exercise the full Runner.Start path; they exercise
// the helper logic that picks the directory passed to VerifyBuildOutput.

// resolveChecksumsDir replicates the dir-resolution logic in
// Runner.Start so it can be tested without standing up a full runner.
// Keep in sync with runner.go § "0b. Verify build output integrity".
func resolveChecksumsDir(workDir string) string {
	outputDir := filepath.Join(workDir, ".forge-output")
	if _, err := os.Stat(filepath.Join(outputDir, "checksums.json")); os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(workDir, "checksums.json")); err == nil {
			return workDir
		}
	}
	return outputDir
}

func TestResolveChecksumsDir_OperatorLayoutPreferred(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".forge-output"), 0755); err != nil {
		t.Fatal(err)
	}
	// Operator-side layout: file under .forge-output/.
	if err := os.WriteFile(filepath.Join(workDir, ".forge-output", "checksums.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	got := resolveChecksumsDir(workDir)
	want := filepath.Join(workDir, ".forge-output")
	if got != want {
		t.Errorf("operator layout: got %q, want %q", got, want)
	}
}

func TestResolveChecksumsDir_FlattenedContainerLayout(t *testing.T) {
	workDir := t.TempDir()
	// Container layout: file at WorkDir root, no .forge-output/ dir.
	if err := os.WriteFile(filepath.Join(workDir, "checksums.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	got := resolveChecksumsDir(workDir)
	if got != workDir {
		t.Errorf("container layout: got %q, want %q", got, workDir)
	}
}

func TestResolveChecksumsDir_NeitherPresent(t *testing.T) {
	workDir := t.TempDir()
	got := resolveChecksumsDir(workDir)
	want := filepath.Join(workDir, ".forge-output")
	if got != want {
		t.Errorf("neither present: got %q, want %q (default to operator layout for VerifyBuildOutput's IsNotExist branch)", got, want)
	}
}

// TestVerifyBuildOutput_ContainerLayoutWorksEndToEnd is the
// integration check: a fully-flattened container directory (WorkDir
// containing checksums.json + the verified file alongside it) passes
// verification when callers feed WorkDir directly into
// VerifyBuildOutput, as the runner now does after the fallback.
func TestVerifyBuildOutput_ContainerLayoutWorksEndToEnd(t *testing.T) {
	workDir := t.TempDir()

	content := []byte("forge: yaml: contents")
	if err := os.WriteFile(filepath.Join(workDir, "forge.yaml"), content, 0644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(content)
	cf := ChecksumsFile{
		Version:   "1",
		Checksums: map[string]string{"forge.yaml": hex.EncodeToString(h[:])},
		Timestamp: "2026-06-10T00:00:00Z",
	}
	data, _ := json.Marshal(cf)
	if err := os.WriteFile(filepath.Join(workDir, "checksums.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	if err := VerifyBuildOutput(resolveChecksumsDir(workDir)); err != nil {
		t.Fatalf("container-layout verification must succeed; got: %v", err)
	}
}
