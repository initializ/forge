package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func testResolver(endpoint string) *pdpResolver {
	return &pdpResolver{
		endpoint:    endpoint,
		token:       "tok",
		orgID:       "org_x",
		workspaceID: "ws_1",
		agentID:     "member-service",
		timeout:     2 * time.Second,
		client:      &http.Client{},
	}
}

// writeEnvelope wraps a decision object in security-next's ApplicationResponse.
func writeEnvelope(w http.ResponseWriter, decisionJSON string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":200,"message":"PDP decision","data":` + decisionJSON + `}`))
}

func hctx(tool, input string) *coreruntime.HookContext {
	return &coreruntime.HookContext{ToolName: tool, ToolInput: input, TaskID: "task-1", CorrelationID: "corr-1"}
}

// S1 — allow, and verify the request shape + auth headers + parsed args + op split.
func TestPDPResolver_Allow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", got)
		}
		if got := r.Header.Get("Org-Id"); got != "org_x" {
			t.Errorf("Org-Id = %q, want org_x", got)
		}
		if got := r.Header.Get("Workspace-Id"); got != "ws_1" {
			t.Errorf("Workspace-Id = %q, want ws_1", got)
		}
		var req pdpRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Tool != "memberservice__reverse_fee" || req.Op != "reverse_fee" {
			t.Errorf("tool/op = %q/%q, want memberservice__reverse_fee/reverse_fee", req.Tool, req.Op)
		}
		if req.Args["amount"] != float64(25) { // parsed, not a string
			t.Errorf("args.amount = %v (%T), want 25 (float64)", req.Args["amount"], req.Args["amount"])
		}
		if req.Caller.Subject != "agent:member-service" || req.Caller.EntitledAccounts != nil {
			t.Errorf("caller = %+v, want subject agent:member-service, no entitled_accounts", req.Caller)
		}
		writeEnvelope(w, `{"decision":"allow","reason":"within policy","policy_version":7}`)
	}))
	defer srv.Close()

	v := testResolver(srv.URL).Resolve(context.Background(), hctx("memberservice__reverse_fee", `{"amount":25,"fee_type":"overdraft"}`))
	if v.Decision != coreruntime.DecisionAllow {
		t.Fatalf("decision = %v, want allow", v.Decision)
	}
	if v.Op != "reverse_fee" {
		t.Errorf("op = %q, want reverse_fee", v.Op)
	}
}

// S2 — defer, with defer_params mapped into a full engine Spec (incl. approvers + parsed timeout).
func TestPDPResolver_Defer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, `{"decision":"defer","reason":"approval required","defer_params":{"to":"platform","timeout":"10m","approvers":["Fee-Approvals@Initializ.ai"],"context":"reverse_fee amount=2500"}}`)
	}))
	defer srv.Close()

	v := testResolver(srv.URL).Resolve(context.Background(), hctx("memberservice__reverse_fee", `{"amount":2500}`))
	if v.Decision != coreruntime.DecisionDefer {
		t.Fatalf("decision = %v, want defer", v.Decision)
	}
	if v.Defer == nil {
		t.Fatal("defer spec is nil")
	}
	if v.Defer.To != "platform" {
		t.Errorf("to = %q, want platform", v.Defer.To)
	}
	if v.Defer.Timeout != 10*time.Minute {
		t.Errorf("timeout = %v, want 10m", v.Defer.Timeout)
	}
	if len(v.Defer.Approvers) != 1 || v.Defer.Approvers[0] != "fee-approvals@initializ.ai" {
		t.Errorf("approvers = %v, want [fee-approvals@initializ.ai] (normalized)", v.Defer.Approvers)
	}
}

// S3 — deny.
func TestPDPResolver_Deny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, `{"decision":"deny","reason":"off-allowlist destination"}`)
	}))
	defer srv.Close()

	v := testResolver(srv.URL).Resolve(context.Background(), hctx("memberservice__get_account_history", `{"destination":"https://evil.example"}`))
	if v.Decision != coreruntime.DecisionDeny {
		t.Fatalf("decision = %v, want deny", v.Decision)
	}
	if v.Reason == "" {
		t.Error("deny reason is empty")
	}
}

// Fail-closed: every failure mode → deny, NEVER allow.
func TestPDPResolver_FailClosed(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		// endpointOverride replaces the test server URL (for the unreachable case)
		endpointOverride string
	}{
		{name: "non-200", handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }},
		{name: "malformed body", handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not json")) }},
		{name: "unknown decision", handler: func(w http.ResponseWriter, r *http.Request) { writeEnvelope(w, `{"decision":"maybe"}`) }},
		{name: "defer without params", handler: func(w http.ResponseWriter, r *http.Request) { writeEnvelope(w, `{"decision":"defer"}`) }},
		{name: "unreachable", endpointOverride: "http://127.0.0.1:1/nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := tc.endpointOverride
			if endpoint == "" {
				srv := httptest.NewServer(tc.handler)
				defer srv.Close()
				endpoint = srv.URL
			}
			v := testResolver(endpoint).Resolve(context.Background(), hctx("some_tool", `{}`))
			if v.Decision != coreruntime.DecisionDeny {
				t.Fatalf("%s: decision = %v, want deny (fail-closed)", tc.name, v.Decision)
			}
		})
	}
}

// A builtin tool (no "__") uses its whole name as the op; empty args → {}.
func TestPDPResolver_BuiltinOpAndEmptyArgs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req pdpRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Tool != "http_request" || req.Op != "http_request" {
			t.Errorf("tool/op = %q/%q, want http_request/http_request", req.Tool, req.Op)
		}
		if req.Args == nil || len(req.Args) != 0 {
			t.Errorf("args = %v, want empty object", req.Args)
		}
		writeEnvelope(w, `{"decision":"allow"}`)
	}))
	defer srv.Close()

	v := testResolver(srv.URL).Resolve(context.Background(), hctx("http_request", ""))
	if v.Decision != coreruntime.DecisionAllow {
		t.Fatalf("decision = %v, want allow", v.Decision)
	}
}

// Unparsable tool args → deny (never sent as a string).
func TestPDPResolver_UnparsableArgsDeny(t *testing.T) {
	v := testResolver("http://unused").Resolve(context.Background(), hctx("t", `{not json`))
	if v.Decision != coreruntime.DecisionDeny {
		t.Fatalf("decision = %v, want deny", v.Decision)
	}
}
