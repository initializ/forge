package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// TestAuditVerify_CleanStreamExitsZero drives the CLI end-to-end
// against a NDJSON stream that was produced by the same audit logger
// on this branch — the OK path.
func TestAuditVerify_CleanStreamExitsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	writeChainedLog(t, path, []string{"a", "b", "c"})

	auditVerifyCmd.SetArgs([]string{path})
	var stdout, stderr bytes.Buffer
	auditVerifyCmd.SetOut(&stdout)
	auditVerifyCmd.SetErr(&stderr)

	if err := auditVerifyRun(auditVerifyCmd, []string{path}); err != nil {
		t.Fatalf("expected clean stream to verify, got %v", err)
	}
	if !strings.Contains(stdout.String(), "OK:") {
		t.Errorf("stdout lacks OK marker: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "3 events") {
		t.Errorf("stdout should count 3 events: %q", stdout.String())
	}
}

// TestAuditVerify_TamperedStreamReports drives the fail path end-to-
// end: a stream with an altered event line makes the CLI return a
// non-nil error AND print the tampering report on stdout.
func TestAuditVerify_TamperedStreamReports(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	writeChainedLog(t, path, []string{"first", "second", "third"})

	raw, err := os.ReadFile(path) //nolint:gosec // test-only
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"event":"second"`, `"event":"SECOND"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	auditVerifyCmd.SetOut(&stdout)
	auditVerifyCmd.SetErr(&stderr)

	err = auditVerifyRun(auditVerifyCmd, []string{path})
	if err == nil {
		t.Fatal("expected error on tampered stream")
	}
	if !strings.Contains(stdout.String(), "TAMPERING DETECTED") {
		t.Errorf("stdout lacks tampering banner: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "line 3") {
		t.Errorf("stdout should report break at line 3 (successor of tampered event): %q", stdout.String())
	}
}

// TestAuditVerify_UnreadablePath returns an OS-shaped error, not a
// crash.
func TestAuditVerify_UnreadablePath(t *testing.T) {
	err := auditVerifyRun(auditVerifyCmd, []string{"/nonexistent/path/to/audit.ndjson"})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "opening") {
		t.Errorf("err lacks path hint: %v", err)
	}
}

func writeChainedLog(t *testing.T, path string, events []string) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec // test-only path in tmp dir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	a := coreruntime.NewAuditLogger(f)
	for _, name := range events {
		a.Emit(coreruntime.AuditEvent{Event: name})
	}
}
