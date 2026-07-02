// Package credentials implements governance R9 — Just-In-Time
// credential dispensing for per-action least privilege.
//
// The design is a two-tier plugin system: a Provider knows how to
// mint short-lived Credentials for a particular backend (AWS STS,
// HashiCorp Vault, RFC 8693 token exchange, etc.); a Credential
// yields a concrete Materialization (env vars, headers) valid for
// exactly one tool invocation.
//
// The runner calls Provider.NewCredential once per (skill, tool)
// pair at startup, then calls Credential.Materialize on every
// BeforeToolExec hook fire. This lets a provider batch expensive
// setup (e.g. AWS credential resolution) once while still giving
// each tool call a fresh scope-down.
//
// See docs/security/least-privilege-credentials.md for the operator
// side.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// CredentialSpec is the declarative shape a skill's config uses to
// describe one JIT credential.
//
// Tool + Binary route the credential to a specific tool call:
//   - Tool empty  → credential applies to every tool.
//   - Tool set    → credential applies only to that tool.
//   - Binary set  → additionally scoped to cli_execute invocations
//     of that binary (ignored for non-cli_execute tools).
//
// Provider names the plugin (e.g. "sts_assume_role", "static").
// Spec is opaque JSON the provider decodes into its own config struct.
type CredentialSpec struct {
	Tool     string          `json:"tool,omitempty" yaml:"tool,omitempty"`
	Binary   string          `json:"binary,omitempty" yaml:"binary,omitempty"`
	Provider string          `json:"provider" yaml:"provider"`
	Spec     json.RawMessage `json:"spec,omitempty" yaml:"spec,omitempty"`
}

// Materialization is what a Credential produces at tool-call time.
//
// Env holds environment variables to inject into a subprocess (for
// cli_execute) or otherwise pass to the tool. Headers is for HTTP
// tool calls that sign or authenticate outbound requests. TTL is the
// operator-facing lifetime of the underlying secret — used for
// audit and to schedule revocation. Revoke, when non-nil, is called
// by the runner after the tool completes.
type Materialization struct {
	Env     map[string]string
	Headers map[string]string
	TTL     Duration
	Revoke  func(context.Context) error
}

// Duration is a JSON-friendly time.Duration wrapper. Values marshal
// as strings like "15m" / "1h" so config files stay readable and
// audit events stay grep-friendly. We deliberately do not export a
// full time.Duration to avoid tying the audit schema to Go's
// nanosecond string form ("15m0s").
type Duration string

// Credential is what a provider hands back from NewCredential — a
// reusable factory the runner calls once per tool invocation.
//
// Implementations MUST be safe for concurrent Materialize calls when
// the same skill's tool is invoked from multiple goroutines. Each
// Materialize call SHOULD produce a distinct credential; providers
// that support caching (STS creds valid for 15m) MAY reuse a
// materialization within its TTL but MUST NOT return one past
// expiration.
type Credential interface {
	// Materialize mints one JIT credential for the given tool call.
	// `args` is the raw JSON the LLM is about to pass to the tool —
	// providers may inspect it to further scope down (e.g. read the
	// S3 key path and constrain the STS session policy). Providers
	// that don't care about args should ignore it.
	Materialize(ctx context.Context, tool string, args json.RawMessage) (Materialization, error)

	// Kind returns the provider name (e.g. "sts_assume_role"). Used
	// on audit events so operators can filter by credential source.
	Kind() string
}

// Provider is the plugin that mints Credentials. One provider
// instance per registered plugin name — the runner looks it up once
// at startup and reuses it for every skill.
type Provider interface {
	// Name returns the plugin name matched against CredentialSpec.Provider.
	Name() string

	// NewCredential decodes spec (the plugin-specific JSON payload)
	// and returns a reusable Credential. Called once per matching
	// CredentialSpec at runner startup.
	NewCredential(ctx context.Context, spec CredentialSpec) (Credential, error)
}

// Registry holds Provider instances by name. A single Registry per
// runtime; providers register themselves at init time by calling
// Register() on the package-level default, or a fresh Registry can
// be constructed for tests.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds p to the registry. Panics on duplicate name — a
// startup misconfiguration bug that should not survive to production.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[p.Name()]; exists {
		panic(fmt.Sprintf("credentials: provider %q registered twice", p.Name()))
	}
	r.providers[p.Name()] = p
}

// Get returns the provider by name. Returns nil when the operator
// referenced a provider that wasn't wired — the caller (runner
// startup) reports this as a config error.
func (r *Registry) Get(name string) Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[name]
}

// Names returns the sorted set of registered provider names. Used
// on startup logs so operators can confirm which plugins are wired.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}

// DefaultRegistry is the package-level registry. Providers that live
// in `credentials/*` subpackages register into this via init(), so
// importing the subpackage is the only wiring an operator has to do.
var DefaultRegistry = NewRegistry()

// ErrUnknownProvider is returned when a CredentialSpec references a
// plugin that wasn't in the Registry.
var ErrUnknownProvider = errors.New("credentials: unknown provider")

// ResolveSpec looks up spec.Provider in r and constructs the
// Credential. Returns ErrUnknownProvider (wrapped) when not registered.
func (r *Registry) ResolveSpec(ctx context.Context, spec CredentialSpec) (Credential, error) {
	p := r.Get(spec.Provider)
	if p == nil {
		return nil, fmt.Errorf("%w: %q (known: %v)", ErrUnknownProvider, spec.Provider, r.Names())
	}
	return p.NewCredential(ctx, spec)
}

// MatchesTool reports whether spec applies to the given tool + binary
// pair. Used by the runner to select the right CredentialSpec from a
// skill's list on each BeforeToolExec fire.
func (spec CredentialSpec) MatchesTool(tool, binary string) bool {
	if spec.Tool != "" && spec.Tool != tool {
		return false
	}
	if spec.Binary != "" && spec.Binary != binary {
		return false
	}
	return true
}
