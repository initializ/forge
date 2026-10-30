package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func TestMigrate(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	src := filepath.Join(work, ".forge", "memory")
	mustWrite(t, filepath.Join(src, "MEMORY.md"), "# mem")
	mustWrite(t, filepath.Join(src, "2026-09-01.md"), "log entry")
	mustWrite(t, filepath.Join(src, "index", "index.json"), "[]")

	sessionRel := filepath.Join("sessions", "2026-09-01.md")

	// Dry run: reports files, writes nothing.
	res, err := MigrateRepoLocal(root, "weather-agent", src, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Files) != 3 {
		t.Fatalf("dry run: dryRun=%v files=%v", res.DryRun, res.Files)
	}
	if _, err := os.Stat(filepath.Join(root, NamespaceAgents)); err == nil {
		t.Error("dry run wrote to destination")
	}
	found := false
	for _, f := range res.Files {
		if f == sessionRel {
			found = true
		}
	}
	if !found {
		t.Errorf("daily log not mapped to sessions/: %v", res.Files)
	}

	// Real run: copies files, maps daily log, leaves source intact.
	if _, err := MigrateRepoLocal(root, "weather-agent", src, false); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, NamespaceAgents, "weather-agent")
	assertExists(t, filepath.Join(dest, "MEMORY.md"))
	assertExists(t, filepath.Join(dest, sessionRel))
	assertExists(t, filepath.Join(dest, "index", "index.json"))
	assertExists(t, filepath.Join(src, migratedSentinel))
	assertExists(t, filepath.Join(src, "MEMORY.md")) // source preserved (copy, not move)

	reg, err := OpenRegistry(root, NamespaceAgents)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := reg.Get("weather-agent"); !ok {
		t.Error("registry not updated after migration")
	}

	// Idempotent: the sentinel makes a second run a no-op.
	res, err = MigrateRepoLocal(root, "weather-agent", src, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Error("expected second run to be skipped")
	}

	// Missing source directory is skipped, not an error.
	res, err = MigrateRepoLocal(root, "x", filepath.Join(work, "does-not-exist"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Error("expected skip on missing source")
	}
}
