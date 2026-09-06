package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Per-site wire pins for X-Workload-Token (agent-identity L1, #444, PR #445
// review): each platform callout is an N-of-N surface, so a future refactor
// dropping StampWorkloadToken at one site must fail a test HERE, not just the
// shared-helper unit tests. These cover the two forge-cli callouts (admission,
// PDP); the platform-token, authorize-URL, and remote-session sites are pinned
// in forge-core.

// activateWorkloadToken points the reader at a temp token file in k8s_sa mode
// and returns the token value the server should observe.
func activateWorkloadToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("wl-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv(coreruntime.EnvWorkloadIdentityMode, coreruntime.WorkloadIdentityModeK8sSA)
	t.Setenv(coreruntime.EnvWorkloadTokenPath, path)
	return "wl-token"
}

func TestPlatformAdmissionChecker_PresentsWorkloadToken(t *testing.T) {
	want := activateWorkloadToken(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(coreruntime.HeaderWorkloadToken)
		_, _ = w.Write([]byte(`{"decision":"admit"}`))
	}))
	defer srv.Close()

	NewPlatformAdmissionChecker(srv.URL, "ag", "org-7", "ws-3", "tok", nil).Admit(context.Background())
	if got != want {
		t.Errorf("admission callout %s = %q, want %q", coreruntime.HeaderWorkloadToken, got, want)
	}
}

func TestPDPResolver_PresentsWorkloadToken(t *testing.T) {
	want := activateWorkloadToken(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(coreruntime.HeaderWorkloadToken)
		writeEnvelope(w, `{"decision":"allow","reason":"ok","policy_version":1}`)
	}))
	defer srv.Close()

	testResolver(srv.URL).Resolve(context.Background(), hctx("svc__op", `{}`))
	if got != want {
		t.Errorf("PDP callout %s = %q, want %q", coreruntime.HeaderWorkloadToken, got, want)
	}
}
