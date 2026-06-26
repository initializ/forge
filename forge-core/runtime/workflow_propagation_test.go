package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWorkflowPropagationMatcher_Matches walks the host-matching
// surface for issue #186 / FORGE-1. Exact entries match a single
// host; wildcard `*.foo.com` entries match any strictly-deeper
// subdomain but NOT the apex; ports are stripped before comparison
// (so a `peer.svc` allow-list entry covers `peer.svc:8443`).
func TestWorkflowPropagationMatcher_Matches(t *testing.T) {
	m := NewWorkflowPropagationMatcher([]string{
		"orchestrator.svc",
		"*.agents.internal",
		"PEER.LOCAL", // upper-case input is normalized
	})

	cases := []struct {
		host string
		want bool
		note string
	}{
		{"orchestrator.svc", true, "exact match"},
		{"orchestrator.svc:8443", true, "exact match strips port"},
		{"peer.local", true, "exact normalized to lower"},
		{"a.agents.internal", true, "wildcard suffix match"},
		{"deep.a.agents.internal", true, "wildcard suffix multi-level"},
		{"agents.internal", false, "wildcard does NOT match apex"},
		{"agents.internal.evil.com", false, "suffix can't be tricked by a longer host"},
		{"not-listed.example.com", false, "no match"},
		{"", false, "empty host"},
	}
	for _, c := range cases {
		t.Run(c.note, func(t *testing.T) {
			if got := m.Matches(c.host); got != c.want {
				t.Errorf("Matches(%q) = %v, want %v (%s)", c.host, got, c.want, c.note)
			}
		})
	}
}

// TestWorkflowPropagationMatcher_IsEmptyAndNilGuard pins the default-
// deploy contract: an empty allow-list (or a nil matcher) MUST report
// IsEmpty and Matches → false. The WrapTransport helper relies on
// IsEmpty to short-circuit the wrap so zero-config deploys pay no
// overhead per request.
func TestWorkflowPropagationMatcher_IsEmptyAndNilGuard(t *testing.T) {
	if !NewWorkflowPropagationMatcher(nil).IsEmpty() {
		t.Errorf("nil-input matcher should be IsEmpty")
	}
	if !NewWorkflowPropagationMatcher([]string{}).IsEmpty() {
		t.Errorf("empty-input matcher should be IsEmpty")
	}
	if !NewWorkflowPropagationMatcher([]string{"", "  "}).IsEmpty() {
		t.Errorf("blank-only-entries matcher should be IsEmpty")
	}
	var m *WorkflowPropagationMatcher
	if !m.IsEmpty() {
		t.Errorf("nil receiver IsEmpty should be true")
	}
	if m.Matches("foo.com") {
		t.Errorf("nil receiver Matches should be false")
	}
}

// recordingRoundTripper records the request it was handed to so a
// test can assert which headers were stamped. The body is discarded
// — these tests only care about headers + URL.
type recordingRoundTripper struct {
	gotReq *http.Request
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.gotReq = req
	return &http.Response{
		StatusCode: 204,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// TestWorkflowPropagationTransport_AppliesHeadersOnAllowlistedHost is
// the core issue #186 invariant: a request to an allow-listed host,
// issued from a context that carries a non-zero WorkflowContext,
// MUST land at the underlying transport with the X-Workflow-* /
// X-Invocation-Caller headers populated.
func TestWorkflowPropagationTransport_AppliesHeadersOnAllowlistedHost(t *testing.T) {
	rec := &recordingRoundTripper{}
	matcher := NewWorkflowPropagationMatcher([]string{"orchestrator.svc"})
	rt := WrapTransportForWorkflowPropagation(rec, matcher)

	wc := WorkflowContext{
		WorkflowID:          "wf-deploy",
		WorkflowExecutionID: "wfrun-001",
		StageID:             "canary",
		StepID:              "bake",
		InvocationCaller:    "orchestrator",
	}
	ctx := WithWorkflowContext(context.Background(), wc)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://orchestrator.svc/v1/dispatch", nil)

	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if rec.gotReq == nil {
		t.Fatal("underlying transport never received the request")
	}
	wantHeaders := map[string]string{
		HeaderWorkflowID:          "wf-deploy",
		HeaderWorkflowExecutionID: "wfrun-001",
		HeaderWorkflowStageID:     "canary",
		HeaderWorkflowStepID:      "bake",
		HeaderInvocationCaller:    "orchestrator",
	}
	for k, want := range wantHeaders {
		if got := rec.gotReq.Header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
}

// TestWorkflowPropagationTransport_OmitsHeadersOnUnlistedHost confirms
// the default-deny posture for any host NOT on the allow-list. The
// safe default — even with a fully-populated WorkflowContext, a
// request to a third-party API like api.openai.com must NOT carry
// the workflow headers, or the operator would silently leak workflow
// identity to a vendor.
func TestWorkflowPropagationTransport_OmitsHeadersOnUnlistedHost(t *testing.T) {
	rec := &recordingRoundTripper{}
	matcher := NewWorkflowPropagationMatcher([]string{"orchestrator.svc"})
	rt := WrapTransportForWorkflowPropagation(rec, matcher)

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{
		WorkflowID:          "wf-deploy",
		WorkflowExecutionID: "wfrun-001",
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.openai.com/v1/chat", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	for _, h := range []string{
		HeaderWorkflowID, HeaderWorkflowExecutionID,
		HeaderWorkflowStageID, HeaderWorkflowStepID, HeaderInvocationCaller,
	} {
		if v := rec.gotReq.Header.Get(h); v != "" {
			t.Errorf("non-allowlisted host leaked header %s=%q", h, v)
		}
	}
}

// TestWorkflowPropagationTransport_NoOpWhenContextIsZero pins the
// "no workflow context = nothing to propagate" path. A direct A2A
// invocation (no orchestrator headers) carries an IsZero
// WorkflowContext; the wrapper must not stamp empty values onto
// outbound headers.
func TestWorkflowPropagationTransport_NoOpWhenContextIsZero(t *testing.T) {
	rec := &recordingRoundTripper{}
	matcher := NewWorkflowPropagationMatcher([]string{"peer.svc"})
	rt := WrapTransportForWorkflowPropagation(rec, matcher)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://peer.svc/health", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	for _, h := range []string{
		HeaderWorkflowID, HeaderWorkflowExecutionID,
		HeaderWorkflowStageID, HeaderWorkflowStepID, HeaderInvocationCaller,
	} {
		if v := rec.gotReq.Header.Get(h); v != "" {
			t.Errorf("empty-ctx allowlisted call leaked header %s=%q", h, v)
		}
	}
}

// TestWrapTransportForWorkflowPropagation_EmptyMatcherShortCircuits
// confirms the zero-overhead default-deploy path: an empty matcher
// returns the underlying transport identity-equal, never wrapping it.
// The reflective check uses interface comparison — if the wrapper had
// been allocated, the returned RoundTripper would NOT equal `rec`.
func TestWrapTransportForWorkflowPropagation_EmptyMatcherShortCircuits(t *testing.T) {
	rec := &recordingRoundTripper{}
	got := WrapTransportForWorkflowPropagation(rec, NewWorkflowPropagationMatcher(nil))
	if got != http.RoundTripper(rec) {
		t.Errorf("empty matcher should return the underlying transport unchanged; got %T", got)
	}
}

// TestWorkflowPropagationTransport_DoesNotMutateOriginalRequest pins
// the http.RoundTripper contract: a wrapper MUST NOT modify the
// caller's req. We mutate headers when applying the workflow set, so
// the wrapper must clone the request. Failing this test would mean
// the caller's req.Header carries the propagated headers after the
// round-trip — a leak across subsequent retries with the SAME req.
func TestWorkflowPropagationTransport_DoesNotMutateOriginalRequest(t *testing.T) {
	rec := &recordingRoundTripper{}
	matcher := NewWorkflowPropagationMatcher([]string{"peer.svc"})
	rt := WrapTransportForWorkflowPropagation(rec, matcher)

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{
		WorkflowID: "wf-x",
	})
	original, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://peer.svc/", nil)
	_, _ = rt.RoundTrip(original)

	if original.Header.Get(HeaderWorkflowID) != "" {
		t.Errorf("wrapper mutated caller's request headers; got %s=%q",
			HeaderWorkflowID, original.Header.Get(HeaderWorkflowID))
	}
}

// TestWorkflowPropagationTransport_EndToEnd is the integration check
// the issue calls out: a stubbed HTTP server records the inbound
// headers, the wrapper is the only thing between an http.Client and
// that server, and the recorded headers carry the workflow ids. This
// confirms the wrapper works end-to-end against a real net/http
// client, not just the recording-transport unit tests.
func TestWorkflowPropagationTransport_EndToEnd(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// The matcher must cover the httptest server's host (127.0.0.1
	// with an ephemeral port). The matcher strips ports, so an
	// exact "127.0.0.1" entry covers any port the test server picks.
	matcher := NewWorkflowPropagationMatcher([]string{"127.0.0.1"})
	client := &http.Client{
		Transport: WrapTransportForWorkflowPropagation(http.DefaultTransport, matcher),
	}

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{
		WorkflowID:          "wf-int",
		WorkflowExecutionID: "exec-int-9",
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	_ = resp.Body.Close()

	if got.Get(HeaderWorkflowID) != "wf-int" {
		t.Errorf("server received WorkflowID=%q, want wf-int", got.Get(HeaderWorkflowID))
	}
	if got.Get(HeaderWorkflowExecutionID) != "exec-int-9" {
		t.Errorf("server received WorkflowExecutionID=%q, want exec-int-9",
			got.Get(HeaderWorkflowExecutionID))
	}
}

// errRoundTripper returns a fixed error on every RoundTrip so we can
// confirm the wrapper propagates underlying-transport failures
// unchanged (no swallowing, no rewrap).
type errRoundTripper struct{ err error }

func (e *errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}

// TestWorkflowPropagationTransport_PropagatesUnderlyingError confirms
// the wrapper is transparent on error: a network error, an egress
// block, a context-cancel — anything the underlying transport
// returns must reach the caller unchanged so existing retry / logging
// logic continues to fire.
func TestWorkflowPropagationTransport_PropagatesUnderlyingError(t *testing.T) {
	want := errors.New("simulated transport failure")
	matcher := NewWorkflowPropagationMatcher([]string{"peer.svc"})
	rt := WrapTransportForWorkflowPropagation(&errRoundTripper{err: want}, matcher)

	ctx := WithWorkflowContext(context.Background(), WorkflowContext{WorkflowID: "wf"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://peer.svc/", nil)
	_, err := rt.RoundTrip(req)
	if !errors.Is(err, want) {
		t.Errorf("RoundTrip err = %v, want %v", err, want)
	}
}

// TestNewWorkflowPropagationMatcher_RejectsBadInputCleanly walks the
// boundary inputs — leading/trailing whitespace, empty strings,
// mixed-case — and confirms the matcher normalizes consistently.
// Spelled out here as a regression pin: an operator who copy-pastes
// a host with a stray trailing space MUST still get a match.
func TestNewWorkflowPropagationMatcher_RejectsBadInputCleanly(t *testing.T) {
	m := NewWorkflowPropagationMatcher([]string{
		"  ORCHESTRATOR.SVC  ",
		"\t*.AGENTS.INTERNAL\n",
		"",
		"   ",
	})
	if !m.Matches("orchestrator.svc") {
		t.Errorf("normalized exact match should hit")
	}
	if !m.Matches("a.agents.internal") {
		t.Errorf("normalized wildcard should hit")
	}
}

// keep the strings import in use across builds where the file may be
// edited; cheap sanity that the package compiles standalone.
var _ = strings.HasPrefix
