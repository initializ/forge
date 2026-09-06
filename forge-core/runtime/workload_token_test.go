package runtime

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for agent-identity L1 workload-token presentation
// (#444 item 1): the token is read FRESH per call from the projected file,
// never cached, gated on WORKLOAD_IDENTITY_MODE=k8s_sa, and the header is
// omitted (not sent empty) when unavailable.

func writeTokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return path
}

func TestWorkloadToken_ReadsFreshWhenK8sSAModeAndFilePresent(t *testing.T) {
	path := writeTokenFile(t, "jwt-abc\n") // projected files carry a trailing newline
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, path)

	if got := WorkloadToken(); got != "jwt-abc" {
		t.Errorf("WorkloadToken() = %q, want %q (trailing newline trimmed)", got, "jwt-abc")
	}
}

func TestWorkloadToken_EmptyWhenModeNotK8sSA(t *testing.T) {
	path := writeTokenFile(t, "jwt-abc")
	t.Setenv(EnvWorkloadTokenPath, path)
	// Mode unset — not a workload-identity deployment.
	t.Setenv(EnvWorkloadIdentityMode, "")
	if got := WorkloadToken(); got != "" {
		t.Errorf("WorkloadToken() = %q, want \"\" when mode != k8s_sa (must not read the file)", got)
	}
	// A different (future SPIRE) mode is also not handled by this path.
	t.Setenv(EnvWorkloadIdentityMode, "attested_workload")
	if got := WorkloadToken(); got != "" {
		t.Errorf("WorkloadToken() = %q, want \"\" for non-k8s_sa mode", got)
	}
}

func TestWorkloadToken_EmptyWhenFileMissing(t *testing.T) {
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, filepath.Join(t.TempDir(), "does-not-exist"))
	if got := WorkloadToken(); got != "" {
		t.Errorf("WorkloadToken() = %q, want \"\" when the token file is absent", got)
	}
}

func TestWorkloadToken_EmptyWhenFileEmpty(t *testing.T) {
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, writeTokenFile(t, "  \n"))
	if got := WorkloadToken(); got != "" {
		t.Errorf("WorkloadToken() = %q, want \"\" for a whitespace-only file", got)
	}
}

func TestWorkloadToken_ReadsFreshOnEachCall_NoCaching(t *testing.T) {
	// The kubelet rotates the projected file in place; a cached value goes
	// stale and the platform's TokenReview rejects it. Rewriting the file
	// between calls must be observed immediately.
	path := writeTokenFile(t, "jwt-v1")
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, path)

	if got := WorkloadToken(); got != "jwt-v1" {
		t.Fatalf("first read = %q, want jwt-v1", got)
	}
	if err := os.WriteFile(path, []byte("jwt-v2"), 0o600); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	if got := WorkloadToken(); got != "jwt-v2" {
		t.Errorf("second read = %q, want jwt-v2 — token must be read fresh, never cached", got)
	}
}

func TestWorkloadToken_EmptyWhenFileOversized(t *testing.T) {
	// The path is operator-controlled; a misconfigured/oversized file must
	// fail closed to "" rather than load an arbitrarily large header value.
	big := make([]byte, maxWorkloadTokenBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, writeTokenFile(t, string(big)))
	if got := WorkloadToken(); got != "" {
		t.Errorf("WorkloadToken() len=%d, want \"\" — oversized file must fail closed", len(got))
	}
}

func TestWorkloadToken_ReadsTokenAtCapBoundary(t *testing.T) {
	// A token exactly at the cap is still read (off-by-one guard).
	tok := strings.Repeat("a", maxWorkloadTokenBytes)
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, writeTokenFile(t, tok))
	if got := WorkloadToken(); got != tok {
		t.Errorf("WorkloadToken() len=%d, want %d (a token exactly at the cap must still read)", len(got), len(tok))
	}
}

func TestStampWorkloadToken_SetsHeaderWhenAvailable(t *testing.T) {
	t.Setenv(EnvWorkloadIdentityMode, WorkloadIdentityModeK8sSA)
	t.Setenv(EnvWorkloadTokenPath, writeTokenFile(t, "jwt-xyz\n"))

	h := http.Header{}
	StampWorkloadToken(h)
	if got := h.Get(HeaderWorkloadToken); got != "jwt-xyz" {
		t.Errorf("%s = %q, want jwt-xyz", HeaderWorkloadToken, got)
	}
}

func TestStampWorkloadToken_OmitsHeaderWhenUnavailable(t *testing.T) {
	// Non-k8s_sa deployment: the header must be ABSENT, not present-and-empty,
	// matching the Org-Id/Workspace-Id tenancy-header contract.
	t.Setenv(EnvWorkloadIdentityMode, "")
	h := http.Header{}
	StampWorkloadToken(h)
	if _, present := h[http.CanonicalHeaderKey(HeaderWorkloadToken)]; present {
		t.Errorf("%s must be omitted entirely when no workload token is available", HeaderWorkloadToken)
	}
}
