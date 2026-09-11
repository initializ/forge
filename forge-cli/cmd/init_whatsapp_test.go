package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-cli/internal/tui/steps"
)

// stagePairedSession writes a stand-in for the session the wizard would have
// paired into a temp store, and returns its path.
func stagePairedSession(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "whatsapp-session.db")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("staging session: %v", err)
	}
	return path
}

func TestRelocateWhatsappSession_NoTokenIsNoop(t *testing.T) {
	opts := &initOptions{EnvVars: map[string]string{}}
	paired, err := relocateWhatsappSession(opts, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if paired {
		t.Error("expected paired=false with no session token")
	}
}

func TestRelocateWhatsappSession_MovesSessionIntoProject(t *testing.T) {
	src := stagePairedSession(t, "pairing-bytes")
	project := t.TempDir()
	opts := &initOptions{EnvVars: map[string]string{steps.WhatsappSessionTokenKey: src}}

	paired, err := relocateWhatsappSession(opts, project)
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if !paired {
		t.Error("expected paired=true")
	}

	dst := filepath.Join(project, whatsappSessionRelativePath)
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading relocated session: %v", err)
	}
	if string(got) != "pairing-bytes" {
		t.Errorf("session content = %q, want the staged bytes", got)
	}
}

// The temp path must never survive into the generated .env — it would leak a
// temp location and be wrong the moment the temp dir is cleaned.
func TestRelocateWhatsappSession_ConsumesSyntheticKey(t *testing.T) {
	src := stagePairedSession(t, "x")
	opts := &initOptions{EnvVars: map[string]string{steps.WhatsappSessionTokenKey: src}}

	if _, err := relocateWhatsappSession(opts, t.TempDir()); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if _, ok := opts.EnvVars[steps.WhatsappSessionTokenKey]; ok {
		t.Error("synthetic session key must be stripped from EnvVars")
	}
}

// The key must be consumed even when relocation fails, or a stale temp path
// reaches .env.
func TestRelocateWhatsappSession_ConsumesKeyOnFailure(t *testing.T) {
	opts := &initOptions{EnvVars: map[string]string{
		steps.WhatsappSessionTokenKey: filepath.Join(t.TempDir(), "absent", "gone.db"),
	}}

	paired, err := relocateWhatsappSession(opts, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a missing session file")
	}
	if paired {
		t.Error("expected paired=false on failure")
	}
	if _, ok := opts.EnvVars[steps.WhatsappSessionTokenKey]; ok {
		t.Error("synthetic key must be stripped even when relocation fails")
	}
}

// The session is the WhatsApp credential; a world-readable copy is a leak.
func TestRelocateWhatsappSession_RelocatedFileIsOwnerOnly(t *testing.T) {
	src := stagePairedSession(t, "secret")
	project := t.TempDir()
	opts := &initOptions{EnvVars: map[string]string{steps.WhatsappSessionTokenKey: src}}

	if _, err := relocateWhatsappSession(opts, project); err != nil {
		t.Fatalf("relocate: %v", err)
	}

	info, err := os.Stat(filepath.Join(project, whatsappSessionRelativePath))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("relocated session mode = %04o, want 0600", perm)
	}
}

func TestRelocateWhatsappSession_RemovesTempDir(t *testing.T) {
	src := stagePairedSession(t, "x")
	tempDir := filepath.Dir(src)
	opts := &initOptions{EnvVars: map[string]string{steps.WhatsappSessionTokenKey: src}}

	if _, err := relocateWhatsappSession(opts, t.TempDir()); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Errorf("temp store should be removed after relocation, stat err = %v", err)
	}
}

// .forge/channels/ does not exist in a fresh project.
func TestRelocateWhatsappSession_CreatesNestedDirs(t *testing.T) {
	src := stagePairedSession(t, "x")
	project := filepath.Join(t.TempDir(), "brand-new")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	opts := &initOptions{EnvVars: map[string]string{steps.WhatsappSessionTokenKey: src}}

	if _, err := relocateWhatsappSession(opts, project); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, whatsappSessionRelativePath)); err != nil {
		t.Errorf("expected nested dirs created: %v", err)
	}
}

// The destination must match session_path in whatsapp-config.yaml.tmpl, or the
// adapter looks somewhere the wizard never wrote.
func TestRelocateWhatsappSession_PathMatchesAdapterDefault(t *testing.T) {
	const want = ".forge/channels/whatsapp-session.db"
	if whatsappSessionRelativePath != want {
		t.Errorf("whatsappSessionRelativePath = %q, want %q (must match whatsapp-config.yaml.tmpl)",
			whatsappSessionRelativePath, want)
	}
}
