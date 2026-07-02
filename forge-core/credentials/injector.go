package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// AuditSink is the narrow interface Injector uses to emit
// credential_issued / credential_revoked / credential_failed events.
// Kept minimal (map-based fields) so the credentials package doesn't
// depend on the runtime package (avoiding an import cycle —
// forge-core/runtime/audit.go already imports agentspec which imports
// this package).
//
// Runner startup wires a small adapter that implements this against
// the real *runtime.AuditLogger.
type AuditSink interface {
	Emit(ctx context.Context, event string, fields map[string]any)
}

// Injector materializes JIT credentials at tool-exec time. Tools that
// need credentials (currently cli_execute) call Materialize(...) and
// merge the resulting env into their subprocess env; the returned
// Handle is used to schedule revocation after the tool completes.
//
// Injector is a thin coordinator over a set of Credential instances
// resolved at startup — the resolution → mint → audit path lives here
// so individual tools don't each duplicate provider lookup + logging.
type Injector struct {
	specs []resolvedSpec
	audit AuditSink
}

// resolvedSpec pairs a CredentialSpec (for matching) with the
// Credential instance the provider handed back at startup.
type resolvedSpec struct {
	spec CredentialSpec
	cred Credential
}

// NewInjector resolves each CredentialSpec against reg and returns
// an Injector. Any spec that references an unregistered provider
// fails startup — runners want a loud config error, not silent
// omission that would leave a tool running without credentials.
func NewInjector(ctx context.Context, reg *Registry, specs []CredentialSpec, audit AuditSink) (*Injector, error) {
	if reg == nil {
		reg = DefaultRegistry
	}
	if audit == nil {
		audit = discardSink{}
	}
	out := make([]resolvedSpec, 0, len(specs))
	for i, s := range specs {
		cred, err := reg.ResolveSpec(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("credentials[%d] (tool=%q binary=%q provider=%q): %w",
				i, s.Tool, s.Binary, s.Provider, err)
		}
		out = append(out, resolvedSpec{spec: s, cred: cred})
	}
	return &Injector{specs: out, audit: audit}, nil
}

// Empty reports whether the injector has any resolved specs. Tools
// can use this to short-circuit the materialize call in the common
// no-JIT-configured case.
func (i *Injector) Empty() bool { return i == nil || len(i.specs) == 0 }

// Materialize looks up the first spec whose Tool+Binary matches, mints
// a fresh Credential via Provider, and returns a Handle carrying the
// Materialization plus a Close func to revoke it. Nil Handle when no
// spec matched — the caller carries on without JIT env.
//
// `args` is the raw JSON the LLM passed the tool; providers can
// inspect it to further scope down (e.g. read the S3 key path).
func (i *Injector) Materialize(ctx context.Context, tool, binary string, args json.RawMessage) (*Handle, error) {
	if i.Empty() {
		return nil, nil
	}
	for _, rs := range i.specs {
		if !rs.spec.MatchesTool(tool, binary) {
			continue
		}
		start := time.Now()
		mat, err := rs.cred.Materialize(ctx, tool, args)
		if err != nil {
			i.audit.Emit(ctx, "credential_failed", map[string]any{
				"provider":    rs.cred.Kind(),
				"tool":        tool,
				"binary":      binary,
				"error":       err.Error(),
				"duration_ms": time.Since(start).Milliseconds(),
			})
			return nil, fmt.Errorf("credentials (%s → tool=%s): %w", rs.cred.Kind(), tool, err)
		}
		i.audit.Emit(ctx, "credential_issued", map[string]any{
			"provider":    rs.cred.Kind(),
			"tool":        tool,
			"binary":      binary,
			"ttl":         string(mat.TTL),
			"env_keys":    sortedKeys(mat.Env),
			"header_keys": sortedKeys(mat.Headers),
			"duration_ms": time.Since(start).Milliseconds(),
		})
		return &Handle{
			mat:       mat,
			kind:      rs.cred.Kind(),
			tool:      tool,
			binary:    binary,
			audit:     i.audit,
			revocable: mat.Revoke != nil,
		}, nil
	}
	return nil, nil
}

// Handle is the per-invocation grip on a materialized credential. The
// caller invokes Close() when the tool has finished so the injector
// can revoke (if applicable) and emit the credential_revoked audit
// event.
type Handle struct {
	mat       Materialization
	kind      string
	tool      string
	binary    string
	audit     AuditSink
	revocable bool
	closed    bool
}

// Env returns the env vars to inject into a subprocess. Never returns
// nil — an empty map is fine to append to a slice.
func (h *Handle) Env() map[string]string {
	if h == nil {
		return nil
	}
	return h.mat.Env
}

// Headers returns the headers to inject on an outbound HTTP call.
func (h *Handle) Headers() map[string]string {
	if h == nil {
		return nil
	}
	return h.mat.Headers
}

// Kind returns the provider name that minted this credential.
func (h *Handle) Kind() string {
	if h == nil {
		return ""
	}
	return h.kind
}

// Close revokes the credential (if the provider supports it) and
// emits credential_revoked. Idempotent — safe to defer.
func (h *Handle) Close(ctx context.Context) error {
	if h == nil || h.closed {
		return nil
	}
	h.closed = true
	fields := map[string]any{
		"provider": h.kind,
		"tool":     h.tool,
		"binary":   h.binary,
	}
	if h.revocable {
		if err := h.mat.Revoke(ctx); err != nil {
			fields["error"] = err.Error()
			h.audit.Emit(ctx, "credential_revoked", fields)
			return err
		}
	}
	h.audit.Emit(ctx, "credential_revoked", fields)
	return nil
}

// sortedKeys returns m's keys sorted — used on audit events so
// output is grep-friendly across runs (map iteration order is
// non-deterministic).
func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// discardSink is the no-op AuditSink used when the caller doesn't
// wire one. Useful in unit tests and for the standalone static
// provider path.
type discardSink struct{}

func (discardSink) Emit(context.Context, string, map[string]any) {}
