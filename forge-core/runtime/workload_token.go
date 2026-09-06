package runtime

import (
	"net/http"
	"os"
	"strings"
)

// Agent workload-identity presentation (agent-identity L1, issue #444, item 1).
//
// When an agent is deployed with WORKLOAD_IDENTITY_MODE=k8s_sa, agent-builder
// provisions a per-agent Kubernetes ServiceAccount and projects an
// audience-bound, kubelet-rotated SA token into a file. Forge presents that
// token on platform-authenticated callouts via the X-Workload-Token header so
// the platform's per-agent entitlement check (§19.13) can bind the call to the
// agent's workload identity — e.g. an agent bound to svc-runbooks can no longer
// fetch svc-security's token.
//
// The token's audience (initializ:platform-token-endpoint) is baked in by the
// kubelet projection; forge only reads and forwards the file, it does not mint.
const (
	// EnvWorkloadIdentityMode gates presentation. Only "k8s_sa" is handled
	// today; SPIRE (attested:workload) is a later phase (#444 item 6).
	EnvWorkloadIdentityMode = "WORKLOAD_IDENTITY_MODE"

	// EnvWorkloadTokenPath overrides the projected-token file location.
	EnvWorkloadTokenPath = "INITIALIZ_WORKLOAD_TOKEN_PATH"

	// DefaultWorkloadTokenPath is where agent-builder projects the token.
	DefaultWorkloadTokenPath = "/var/run/secrets/initializ.ai/workload/token"

	// HeaderWorkloadToken carries the projected SA token to the platform.
	HeaderWorkloadToken = "X-Workload-Token"

	// WorkloadIdentityModeK8sSA is the k8s ServiceAccount-token mode.
	WorkloadIdentityModeK8sSA = "k8s_sa"
)

// WorkloadToken reads the projected ServiceAccount token FRESH from the
// configured path and returns it, or "" when workload identity is not active
// or no token is available.
//
// It is read on every call and never cached: the kubelet rotates the file in
// place, so a cached value goes stale and the platform's TokenReview rejects it
// (the "no_token" failure class the L1 contract warns about).
//
// Returns "" (header omitted downstream) when:
//   - WORKLOAD_IDENTITY_MODE != "k8s_sa" (not a workload-identity deployment), or
//   - the token file is absent, unreadable, or empty.
//
// The empty case is the normal path for non-k8s_sa deployments; presentation is
// additive and never blocks a callout.
func WorkloadToken() string {
	if os.Getenv(EnvWorkloadIdentityMode) != WorkloadIdentityModeK8sSA {
		return ""
	}
	path := os.Getenv(EnvWorkloadTokenPath)
	if path == "" {
		path = DefaultWorkloadTokenPath
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Projected token files carry a trailing newline; trim so the header value
	// is the bare JWT.
	return strings.TrimSpace(string(b))
}

// StampWorkloadToken sets the X-Workload-Token header from the freshly-read
// projected SA token. When no token is available it leaves the header unset —
// matching the tenancy-header contract (Org-Id/Workspace-Id are omitted rather
// than sent empty), so the platform distinguishes "unset" from "empty".
//
// Call this at every platform-authenticated callout, right after the
// Authorization + tenancy headers are stamped.
func StampWorkloadToken(h http.Header) {
	if tok := WorkloadToken(); tok != "" {
		h.Set(HeaderWorkloadToken, tok)
	}
}
