package forgeui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMediaFile drops a file into the agent's .forge/files/<subdir> store.
func writeMediaFile(t *testing.T, agentDir, subdir, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(agentDir, ".forge", "files", subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func getFile(t *testing.T, s *UIServer, agentID, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/"+agentID+"/files/"+name, nil)
	req.SetPathValue("id", agentID)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	s.handleGetFile(rec, req)
	return rec
}

func TestHandleGetFile_ServesGeneratedAndInbound(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "test-agent")
	// Running state: handleGetFile resolves the agent via the scanner, which
	// finds it by its forge.yaml — no port needed for file serving.
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	writeMediaFile(t, agentDir, "generated", "abc123.png", png)
	writeMediaFile(t, agentDir, "inbound", "def456.jpg", []byte{0xff, 0xd8, 0xff, 0x00})

	rec := getFile(t, s, "test-agent", "abc123.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("generated file: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png (inline-servable)", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options: nosniff on all responses")
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Error("a known image type should be served inline, not as an attachment")
	}
	if rec.Body.Len() != len(png) {
		t.Errorf("body len = %d, want %d", rec.Body.Len(), len(png))
	}

	if rec := getFile(t, s, "test-agent", "def456.jpg"); rec.Code != http.StatusOK {
		t.Fatalf("inbound file: got %d, want 200", rec.Code)
	}
}

// A non-media type (e.g. a stray svg/html) must be forced to a download
// attachment with a generic type + nosniff, so a direct URL hit can't render
// active content in the dashboard origin.
func TestHandleGetFile_NonMediaServedAsAttachment(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "test-agent")
	writeMediaFile(t, agentDir, "generated", "evil.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))

	rec := getFile(t, s, "test-agent", "evil.svg")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream (non-media must not serve inline)", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options: nosniff")
	}
}

// A symlink planted inside the store pointing outward passes the lexical name
// check but must be caught by symlink re-confinement (#528 parity).
func TestHandleGetFile_RejectsSymlinkEscape(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "test-agent")

	secret := filepath.Join(dir, "secret.png")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	genDir := filepath.Join(agentDir, ".forge", "files", "generated")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(genDir, "leak.png")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	rec := getFile(t, s, "test-agent", "leak.png")
	if rec.Code == http.StatusOK {
		t.Errorf("a symlink escaping the store must not be served, got 200 body %q", rec.Body.String())
	}
}

func TestHandleGetFile_NotFound(t *testing.T) {
	s, dir := newTestServer(t)
	createTestAgent(t, dir, "test-agent")
	if rec := getFile(t, s, "test-agent", "missing.png"); rec.Code != http.StatusNotFound {
		t.Errorf("missing file: got %d, want 404", rec.Code)
	}
}

func TestHandleGetFile_RejectsTraversal(t *testing.T) {
	s, dir := newTestServer(t)
	agentDir := createTestAgent(t, dir, "test-agent")
	// A secret OUTSIDE the files store the handler must never serve.
	if err := os.WriteFile(filepath.Join(agentDir, "forge.yaml.secret"), []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"..%2f..%2fforge.yaml", "..", "a/b", "....//x", "/etc/passwd"} {
		rec := getFile(t, s, "test-agent", bad)
		if rec.Code == http.StatusOK {
			t.Errorf("name %q should be rejected, got 200 with body %q", bad, rec.Body.String())
		}
	}
}
