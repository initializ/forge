package runtime

import (
	"net/http"
	"strings"
)

// Workflow auto-propagation matcher + transport wrapper (issue #186 /
// FORGE-1). Auto-propagation is OFF by default so the X-Workflow-* /
// X-Invocation-Caller headers — which identify the workflow run — do
// not leak to third-party APIs when an agent calls them as a tool.
// Operators opt specific downstream hosts in via the
// `workflow_propagation.allowed_hosts` block in forge.yaml; matched
// hosts auto-receive the headers, unmatched hosts continue to require
// an explicit `WorkflowContextFromContext(ctx).ApplyToHTTPHeaders(...)`
// call from the tool.
//
// Why this is a transport wrapper, not a per-tool change: every
// built-in HTTP tool (http_request, webhook_call, web_search_*, …)
// already routes its requests through the egress transport installed
// on the request context. Wrapping that transport once at the runner
// startup point picks up every current tool and every future tool
// without touching them. The matcher short-circuits when no hosts
// are configured (the default-deploy path), so the wrapper is
// zero-overhead unless the operator opts in.

// WorkflowPropagationMatcher decides whether a given outbound host
// should auto-receive the workflow headers. Mirrors the wildcard
// semantics of security.DomainMatcher (exact + `*.suffix.com`) but
// is kept independent so this package doesn't grow a dependency on
// forge-core/security just for matching.
type WorkflowPropagationMatcher struct {
	exact    map[string]bool
	suffixes []string // ".foo.com" — leading dot kept so `bar.foo.com` matches but `foo.com` doesn't
}

// NewWorkflowPropagationMatcher parses an allow-list spec into a
// matcher. Empty / nil input returns a matcher whose Matches() always
// returns false — explicit safe default. Each entry is normalized to
// lowercase and trimmed; blank entries are skipped.
//
// Entries beginning with `*.` register as wildcard suffix patterns;
// every other entry is an exact host. There is deliberately no other
// pattern syntax — Forge's hostname surface is small enough that
// exact + suffix covers the meaningful cases and matches the existing
// egress allow-list shape.
func NewWorkflowPropagationMatcher(hosts []string) *WorkflowPropagationMatcher {
	m := &WorkflowPropagationMatcher{exact: map[string]bool{}}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if strings.HasPrefix(h, "*.") {
			// "*.github.com" → suffix ".github.com" — keeping the
			// leading dot means HasSuffix matches a strictly-deeper
			// subdomain and never the bare apex.
			m.suffixes = append(m.suffixes, h[1:])
			continue
		}
		m.exact[h] = true
	}
	return m
}

// Matches reports whether the given host is in the allow-list. The
// host argument may include a `:port` suffix (e.g. URL.Host on a
// custom-port request) — the matcher strips the port before
// comparing because propagation decisions are per-host, not per-port.
//
// A nil receiver returns false so callers can use the matcher pattern
// without nil-guarding: `m.Matches(host)` is safe even on an
// uninitialized matcher.
func (m *WorkflowPropagationMatcher) Matches(host string) bool {
	if m == nil {
		return false
	}
	// Strip `:port`. Bracketed IPv6 hosts (`[::1]:8080`) are not in
	// scope for the allow-list — workflow propagation targets are
	// named services in production deploys.
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host, "]") {
		host = host[:i]
	}
	host = strings.ToLower(host)
	if m.exact[host] {
		return true
	}
	for _, suffix := range m.suffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// IsEmpty reports whether the matcher would never match any host
// (no exact entries, no wildcards). Lets callers short-circuit the
// transport wrap entirely in the default-deploy path.
func (m *WorkflowPropagationMatcher) IsEmpty() bool {
	if m == nil {
		return true
	}
	return len(m.exact) == 0 && len(m.suffixes) == 0
}

// workflowPropagationTransport wraps an http.RoundTripper and applies
// workflow headers to outbound requests whose host matches the
// configured allow-list. The wrapper reads the WorkflowContext from
// the REQUEST's context (req.Context()) — that's the context the
// agent loop installs on every tool call, so the wrapper picks up
// the orchestrator's workflow ids automatically.
type workflowPropagationTransport struct {
	underlying http.RoundTripper
	matcher    *WorkflowPropagationMatcher
}

// RoundTrip applies the workflow headers when the host is allow-listed
// AND the request context carries a non-zero WorkflowContext. The
// underlying transport handles the actual round-trip — this wrapper
// only mutates headers and never blocks the request.
//
// Headers are applied by ApplyToHTTPHeaders, which Set()s each
// non-empty WorkflowContext field — that means a manual
// ApplyToHTTPHeaders call from a tool followed by this wrapper is
// idempotent (Set, not Add). The reverse order produces the same
// result.
func (t *workflowPropagationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.matcher != nil && t.matcher.Matches(req.URL.Host) {
		if wc := WorkflowContextFromContext(req.Context()); !wc.IsZero() {
			// Cloning the request is the http.RoundTripper contract
			// — RoundTrip MUST NOT modify the passed-in request. We
			// only ever rewrite headers, so a shallow clone with a
			// cloned header map is enough.
			req2 := req.Clone(req.Context())
			wc.ApplyToHTTPHeaders(req2.Header)
			req = req2
		}
	}
	underlying := t.underlying
	if underlying == nil {
		underlying = http.DefaultTransport
	}
	return underlying.RoundTrip(req)
}

// WrapTransportForWorkflowPropagation wraps an existing RoundTripper
// so requests targeting allow-listed hosts auto-receive workflow
// headers from the request context. Returns the underlying transport
// unchanged when the matcher is empty — the default-deploy zero-
// overhead path (no extra goroutine hops, no extra allocations per
// request when nothing is configured).
//
// Hook this from the runner once, around the egress transport, before
// the resulting client is stashed onto the request context via
// security.WithEgressClient — every HTTP tool that reads the egress
// transport from context (which is all of them) picks up the
// auto-apply transparently. Issue #186.
func WrapTransportForWorkflowPropagation(rt http.RoundTripper, matcher *WorkflowPropagationMatcher) http.RoundTripper {
	if matcher.IsEmpty() {
		return rt
	}
	return &workflowPropagationTransport{underlying: rt, matcher: matcher}
}
