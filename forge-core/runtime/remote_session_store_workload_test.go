package runtime

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Per-site wire pin for X-Workload-Token on the remote session store
// (agent-identity L1, #444, PR #445 review). setHeaders is shared across
// Load/Save/Delete, so pinning one path guards them all.
func TestRemoteSessionStore_PresentsWorkloadToken(t *testing.T) {
	tokPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokPath, []byte("wl-sess\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, tokPath)

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(HeaderWorkloadToken)
		w.WriteHeader(http.StatusNotFound) // Load treats 404 as "no session" (nil, nil)
	}))
	defer srv.Close()

	// Load stamps setHeaders before the GET; the 404 return is irrelevant.
	_, _ = newTestRemoteStore(srv.URL).Load("task-x")
	if got != "wl-sess" {
		t.Errorf("session-store callout %s = %q, want wl-sess", HeaderWorkloadToken, got)
	}
}
