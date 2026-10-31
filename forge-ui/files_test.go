package forgeui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Error("expected a Content-Type header")
	}
	if rec.Body.Len() != len(png) {
		t.Errorf("body len = %d, want %d", rec.Body.Len(), len(png))
	}

	if rec := getFile(t, s, "test-agent", "def456.jpg"); rec.Code != http.StatusOK {
		t.Fatalf("inbound file: got %d, want 200", rec.Code)
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
