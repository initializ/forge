// Package static is the reference no-op Credential provider for
// governance R9.
//
// It maps a CredentialSpec straight to an env-var map — no expiration,
// no revocation, no external calls. Two uses:
//   - Default fallback so skills that don't declare a JIT provider
//     still get an explicit CredentialSpec-shaped path (audit
//     visibility, one consistent code path).
//   - Test fixture — unit tests wire a static provider and inspect
//     the injected env without pulling in AWS SDKs or mock servers.
//
// Not suitable for production least-privilege scoping — for that,
// use sts_assume_role or a Vault provider.
package static

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/initializ/forge/forge-core/credentials"
)

// ProviderName is the string used in CredentialSpec.Provider.
const ProviderName = "static"

// Spec is the plugin-specific config decoded from CredentialSpec.Spec.
// A minimal shape — env vars, optional headers, and an operator-
// declared TTL for audit purposes only (the values themselves never
// expire).
type Spec struct {
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	TTL     string            `json:"ttl,omitempty"`
}

// Provider implements credentials.Provider.
type Provider struct{}

// Name returns the plugin name.
func (Provider) Name() string { return ProviderName }

// NewCredential decodes spec.Spec into a Spec and returns a Credential
// closed over it.
func (Provider) NewCredential(_ context.Context, cs credentials.CredentialSpec) (credentials.Credential, error) {
	var s Spec
	if len(cs.Spec) > 0 {
		if err := json.Unmarshal(cs.Spec, &s); err != nil {
			return nil, fmt.Errorf("static provider: decoding spec: %w", err)
		}
	}
	return &Credential{spec: s}, nil
}

// Credential is the materializer returned by Provider.NewCredential.
type Credential struct {
	spec Spec
}

// Kind returns the provider name for audit-event tagging.
func (Credential) Kind() string { return ProviderName }

// Materialize returns the operator-declared env/headers. No external
// I/O, no error path.
func (c *Credential) Materialize(_ context.Context, _ string, _ json.RawMessage) (credentials.Materialization, error) {
	env := cloneMap(c.spec.Env)
	headers := cloneMap(c.spec.Headers)
	return credentials.Materialization{
		Env:     env,
		Headers: headers,
		TTL:     credentials.Duration(c.spec.TTL),
	}, nil
}

// cloneMap copies a map so callers can't mutate the provider's config
// through the returned Materialization.
func cloneMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}

func init() {
	credentials.DefaultRegistry.Register(Provider{})
}
