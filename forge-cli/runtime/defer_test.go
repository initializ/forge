package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	deferengine "github.com/initializ/forge/forge-core/security/deferpolicy"
	"github.com/initializ/forge/forge-core/types"
)

// fakeStatusStore is a tiny in-memory TaskStatusStore for hook tests
// so they don't need a full *a2a.TaskStore + Runner + server.
type fakeStatusStore struct {
	mu   sync.Mutex
	last map[string]a2a.TaskStatus
	seen []a2a.TaskStatus
}

func newFakeStatusStore() *fakeStatusStore {
	return &fakeStatusStore{last: map[string]a2a.TaskStatus{}}
}

func (f *fakeStatusStore) SetStatus(id string, s a2a.TaskStatus) a2a.TaskStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := f.last[id]
	f.last[id] = s
	f.seen = append(f.seen, s)
	return prev
}

func (f *fakeStatusStore) statuses() []a2a.TaskState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]a2a.TaskState, len(f.seen))
	for i, s := range f.seen {
		out[i] = s.State
	}
	return out
}

// buildTestRunner is a minimal Runner shell for exercising the defer
// hook — we only populate what the hook actually reads. Skips the
// whole executor / server construction path.
func buildTestRunner(t *testing.T, deferCfg types.DeferConfig) (*Runner, *coreruntime.AuditLogger, *fakeStatusStore, *coreruntime.HookRegistry) {
	t.Helper()
	logger := coreruntime.NewJSONLogger(discardWriter{}, false)
	auditLogger := coreruntime.NewAuditLogger(discardWriter{})
	r := &Runner{
		logger: logger,
		cfg: RunnerConfig{
			Config: &types.ForgeConfig{
				Security: types.SecurityConfig{Defer: deferCfg},
			},
		},
		deferEngine: deferengine.New(),
	}
	hooks := coreruntime.NewHookRegistry()
	store := newFakeStatusStore()
	r.registerDeferHook(hooks, store, auditLogger)
	return r, auditLogger, store, hooks
}

// TestDeferHook_ApprovePath is the happy path: hook fires, task
// status flips to Deferred, approver POSTs approve, hook returns
// nil (tool proceeds), status flips back to whatever it was.
func TestDeferHook_ApprovePath(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled:        true,
		DefaultTimeout: 500 * time.Millisecond,
		Tools: map[string]types.DeferToolConfig{
			"cli_execute": {To: "human:oncall", Timeout: 500 * time.Millisecond},
		},
	}
	r, _, store, hooks := buildTestRunner(t, cfg)

	// Fire the hook in a goroutine — it'll block waiting for the
	// decision. Feed approve after a beat.
	hctx := &coreruntime.HookContext{
		ToolName:      "cli_execute",
		ToolInput:     `{"binary":"aws","args":["s3","ls"]}`,
		TaskID:        "task-approve",
		CorrelationID: "corr-1",
	}
	done := make(chan error, 1)
	go func() {
		done <- hooks.Fire(context.Background(), coreruntime.BeforeToolExec, hctx)
	}()
	time.Sleep(30 * time.Millisecond)

	// Resolve with approve.
	if err := r.deferEngine.Resolve("task-approve", deferengine.Resolution{
		Decision: deferengine.DecisionApprove,
		Approver: "alice",
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("hook returned err on approve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not return within 2s")
	}

	// Status flips seen by the store: Deferred while blocked, then
	// restored (to zero value since the fake had none). Check the
	// Deferred flip actually happened.
	saw := store.statuses()
	found := false
	for _, s := range saw {
		if s == a2a.TaskStateDeferred {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected TaskStateDeferred in status flips, got %v", saw)
	}
}

// TestDeferHook_RejectPath — reject arrives, hook returns error
// mentioning approver + note.
func TestDeferHook_RejectPath(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled: true,
		Tools: map[string]types.DeferToolConfig{
			"cli_execute": {To: "human:oncall", Timeout: 500 * time.Millisecond},
		},
	}
	r, _, _, hooks := buildTestRunner(t, cfg)

	hctx := &coreruntime.HookContext{
		ToolName: "cli_execute",
		TaskID:   "task-reject",
	}
	done := make(chan error, 1)
	go func() {
		done <- hooks.Fire(context.Background(), coreruntime.BeforeToolExec, hctx)
	}()
	time.Sleep(30 * time.Millisecond)

	_ = r.deferEngine.Resolve("task-reject", deferengine.Resolution{
		Decision: deferengine.DecisionReject,
		Approver: "bob",
		Note:     "too risky in prod",
	})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hook returned nil on reject")
		}
		if !strings.Contains(err.Error(), "rejected by bob") {
			t.Errorf("error missing approver: %v", err)
		}
		if !strings.Contains(err.Error(), "too risky") {
			t.Errorf("error missing note: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not return within 2s")
	}
}

// TestDeferHook_TimeoutPath — no decision arrives, timeout auto-
// denies. Uses a very short timeout so the test wall-clock is cheap.
func TestDeferHook_TimeoutPath(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled: true,
		Tools: map[string]types.DeferToolConfig{
			"cli_execute": {To: "human:oncall", Timeout: 30 * time.Millisecond},
		},
	}
	_, _, _, hooks := buildTestRunner(t, cfg)

	hctx := &coreruntime.HookContext{ToolName: "cli_execute", TaskID: "task-timeout"}
	done := make(chan error, 1)
	go func() {
		done <- hooks.Fire(context.Background(), coreruntime.BeforeToolExec, hctx)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hook returned nil on timeout")
		}
		if !strings.Contains(err.Error(), "auto-deny") {
			t.Errorf("error should mention auto-deny: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not time out within 2s")
	}
}

// TestDeferHook_UnconfiguredToolFastPath — tools not in the config
// pass through the hook without blocking.
func TestDeferHook_UnconfiguredToolFastPath(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled: true,
		Tools: map[string]types.DeferToolConfig{
			"cli_execute": {To: "x", Timeout: 5 * time.Second},
		},
	}
	_, _, _, hooks := buildTestRunner(t, cfg)

	hctx := &coreruntime.HookContext{ToolName: "web_search", TaskID: "task-other"}
	// Should return immediately with no error.
	start := time.Now()
	if err := hooks.Fire(context.Background(), coreruntime.BeforeToolExec, hctx); err != nil {
		t.Fatalf("unconfigured tool should pass through: %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("unconfigured tool took too long: %s", time.Since(start))
	}
}

// TestDeferHook_ContextCancelledDuringBlock — the HTTP client
// disconnects mid-defer; hook must return the ctx error and clean
// up the pending deferral so the engine doesn't leak.
func TestDeferHook_ContextCancelledDuringBlock(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled: true,
		Tools: map[string]types.DeferToolConfig{
			"cli_execute": {To: "x", Timeout: 5 * time.Second},
		},
	}
	r, _, _, hooks := buildTestRunner(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	hctx := &coreruntime.HookContext{ToolName: "cli_execute", TaskID: "task-cancel"}
	done := make(chan error, 1)
	go func() {
		done <- hooks.Fire(ctx, coreruntime.BeforeToolExec, hctx)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hook returned nil on ctx cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not return on ctx cancel within 2s")
	}
	// The pending deferral should be cleaned up.
	if r.deferEngine.Pending() != 0 {
		t.Errorf("Pending after cancel: got %d want 0", r.deferEngine.Pending())
	}
}

// TestResolveDeferSpec_ContextTemplate — {tool} and {args} placeholders
// expand into the audit context. Rendered value is what a Slack
// notifier would show the approver.
func TestResolveDeferSpec_ContextTemplate(t *testing.T) {
	hctx := &coreruntime.HookContext{
		ToolName:  "cli_execute",
		ToolInput: `{"binary":"aws","args":["s3","rm","--recursive","s3://prod/"]}`,
	}
	cfg := types.DeferConfig{}
	tool := types.DeferToolConfig{
		ContextTemplate: "Agent wants to run {tool} with args: {args}",
	}
	spec := resolveDeferSpec(cfg, tool, hctx)
	if !strings.Contains(spec.ContextForApprover, "cli_execute") {
		t.Errorf("template missed {tool}: %s", spec.ContextForApprover)
	}
	if !strings.Contains(spec.ContextForApprover, `"binary":"aws"`) {
		t.Errorf("template missed {args}: %s", spec.ContextForApprover)
	}
}

// TestDecisionsEndpoint_HappyPath drives the HTTP POST directly
// (bypassing server routing) so we can assert 200 + body without
// starting the full A2A server.
func TestDecisionsEndpoint_HappyPath(t *testing.T) {
	// Register a pending deferral so the endpoint has something to
	// resolve.
	engine := deferengine.New()
	h, err := engine.Register("task-http", "cli_execute", deferengine.Spec{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := h.WaitCtx(context.Background())
		waitDone <- err
	}()

	// Manually invoke the endpoint's logic via a small copy of the
	// runner's handler (the real one closes over srv which we don't
	// have here). Same shape.
	body := map[string]string{"decision": "approve", "approver": "alice", "note": "ok"}
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/tasks/task-http/decisions", strings.NewReader(string(bodyJSON)))
	req.SetPathValue("id", "task-http")
	rec := httptest.NewRecorder()

	handler := makeDecisionsHandler(engine, coreruntime.NewAuditLogger(discardWriter{}))
	handler(rec, req)

	if rec.Code != 200 {
		t.Errorf("status: got %d want 200; body: %s", rec.Code, rec.Body.String())
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Errorf("wait returned err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not release within 2s of approve POST")
	}
}

// TestDecisionsEndpoint_NotFound — POST for an unknown task ID.
func TestDecisionsEndpoint_NotFound(t *testing.T) {
	engine := deferengine.New()
	body, _ := json.Marshal(map[string]string{"decision": "approve"})
	req := httptest.NewRequest("POST", "/tasks/nope/decisions", strings.NewReader(string(body)))
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	makeDecisionsHandler(engine, coreruntime.NewAuditLogger(discardWriter{}))(rec, req)
	if rec.Code != 404 {
		t.Errorf("status: got %d want 404", rec.Code)
	}
}

// TestDecisionsEndpoint_BadDecision — invalid decision string
// (neither approve nor reject) is rejected 400.
func TestDecisionsEndpoint_BadDecision(t *testing.T) {
	engine := deferengine.New()
	_, _ = engine.Register("task-x", "cli_execute", deferengine.Spec{Timeout: 5 * time.Second})
	body, _ := json.Marshal(map[string]string{"decision": "maybe"})
	req := httptest.NewRequest("POST", "/tasks/task-x/decisions", strings.NewReader(string(body)))
	req.SetPathValue("id", "task-x")
	rec := httptest.NewRecorder()
	makeDecisionsHandler(engine, coreruntime.NewAuditLogger(discardWriter{}))(rec, req)
	if rec.Code != 400 {
		t.Errorf("status: got %d want 400", rec.Code)
	}
	// Cleanup — the pending deferral would leak otherwise.
	_ = engine.Resolve("task-x", deferengine.Resolution{Decision: deferengine.DecisionTimeout})
}

// TestDeferHook_FiresDeferralNotifier pins the #310 wiring: when a tool call is
// deferred and a DeferralNotifier is set, the hook invokes it with the routing
// target + task/tool/context, so a channel adapter can deliver the approval.
func TestDeferHook_FiresDeferralNotifier(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled:        true,
		DefaultTimeout: 500 * time.Millisecond,
		Tools: map[string]types.DeferToolConfig{
			"cli_execute": {To: "channel:slack:#oncall", Timeout: 500 * time.Millisecond,
				ContextTemplate: "run {tool}"},
		},
	}
	r, _, _, hooks := buildTestRunner(t, cfg)

	type call struct{ to, taskID, tool, ctx string }
	got := make(chan call, 1)
	r.SetDeferralNotifier(func(_ context.Context, to, taskID, tool, approverCtx string, _ time.Duration) error {
		got <- call{to, taskID, tool, approverCtx}
		return nil
	})

	hctx := &coreruntime.HookContext{ToolName: "cli_execute", ToolInput: `{}`, TaskID: "task-notify"}
	done := make(chan error, 1)
	go func() { done <- hooks.Fire(context.Background(), coreruntime.BeforeToolExec, hctx) }()

	select {
	case c := <-got:
		if c.to != "channel:slack:#oncall" || c.taskID != "task-notify" || c.tool != "cli_execute" || c.ctx != "run cli_execute" {
			t.Errorf("notifier got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deferral notifier was not invoked")
	}

	// Unblock the hook so the goroutine exits cleanly.
	_ = r.deferEngine.Resolve("task-notify", deferengine.Resolution{Decision: deferengine.DecisionApprove})
	<-done
}

// TestDeferHook_NotifierFailureNonFatal: a delivery failure must NOT deny — the
// hook still blocks and a direct approve still lets the tool proceed.
func TestDeferHook_NotifierFailureNonFatal(t *testing.T) {
	cfg := types.DeferConfig{
		Enabled: true, DefaultTimeout: 500 * time.Millisecond,
		Tools: map[string]types.DeferToolConfig{"cli_execute": {To: "channel:slack:#x", Timeout: 500 * time.Millisecond}},
	}
	r, _, _, hooks := buildTestRunner(t, cfg)
	r.SetDeferralNotifier(func(context.Context, string, string, string, string, time.Duration) error {
		return errTestDelivery
	})

	hctx := &coreruntime.HookContext{ToolName: "cli_execute", ToolInput: `{}`, TaskID: "task-nf"}
	done := make(chan error, 1)
	go func() { done <- hooks.Fire(context.Background(), coreruntime.BeforeToolExec, hctx) }()
	time.Sleep(30 * time.Millisecond)
	_ = r.deferEngine.Resolve("task-nf", deferengine.Resolution{Decision: deferengine.DecisionApprove})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delivery failure must not fail the hook on approve; got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not return")
	}
}

var errTestDelivery = fmt.Errorf("simulated delivery failure")

// TestDecisionsEndpoint_ApproverAllowlist covers #313 enforcement at the
// decisions endpoint: allowlisted email resolves (case-insensitive),
// non-listed is refused (deferral stays pending), empty email fails closed,
// and no allowlist allows anyone.
func TestDecisionsEndpoint_ApproverAllowlist(t *testing.T) {
	post := func(engine *deferengine.Engine, taskID, email string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"decision": "approve", "approver": "who", "approver_email": email})
		req := httptest.NewRequest("POST", "/tasks/"+taskID+"/decisions", strings.NewReader(string(body)))
		req.SetPathValue("id", taskID)
		rec := httptest.NewRecorder()
		makeDecisionsHandler(engine, coreruntime.NewAuditLogger(discardWriter{}))(rec, req)
		return rec
	}
	allow := deferengine.Spec{Timeout: 5 * time.Second, Approvers: []string{"alice@corp.com"}}

	t.Run("allowlisted email resolves (case-insensitive)", func(t *testing.T) {
		engine := deferengine.New()
		_, _ = engine.Register("t1", "cli_execute", allow)
		if rec := post(engine, "t1", "Alice@Corp.com"); rec.Code != 200 {
			t.Errorf("allowlisted approver: got %d want 200; %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-listed refused, deferral stays pending", func(t *testing.T) {
		engine := deferengine.New()
		_, _ = engine.Register("t2", "cli_execute", allow)
		if rec := post(engine, "t2", "mallory@evil.com"); rec.Code != 403 {
			t.Errorf("non-listed approver: got %d want 403", rec.Code)
		}
		if _, ok := engine.Peek("t2"); !ok {
			t.Error("deferral must stay pending after a refused approval")
		}
		_ = engine.Resolve("t2", deferengine.Resolution{Decision: deferengine.DecisionTimeout})
	})

	t.Run("empty email fails closed", func(t *testing.T) {
		engine := deferengine.New()
		_, _ = engine.Register("t3", "cli_execute", allow)
		if rec := post(engine, "t3", ""); rec.Code != 403 {
			t.Errorf("empty email vs allowlist must fail closed: got %d want 403", rec.Code)
		}
		_ = engine.Resolve("t3", deferengine.Resolution{Decision: deferengine.DecisionTimeout})
	})

	t.Run("no allowlist allows anyone", func(t *testing.T) {
		engine := deferengine.New()
		_, _ = engine.Register("t4", "cli_execute", deferengine.Spec{Timeout: 5 * time.Second})
		if rec := post(engine, "t4", ""); rec.Code != 200 {
			t.Errorf("no allowlist: got %d want 200", rec.Code)
		}
	})
}

// TestResolveDeferSpec_Approvers pins normalization + the default fallback.
func TestResolveDeferSpec_Approvers(t *testing.T) {
	hctx := &coreruntime.HookContext{ToolName: "cli_execute", ToolInput: "{}"}

	t.Run("per-tool, normalized", func(t *testing.T) {
		spec := resolveDeferSpec(types.DeferConfig{}, types.DeferToolConfig{
			Approvers: []string{"  Alice@Corp.com ", "BOB@corp.com", "  "},
		}, hctx)
		want := []string{"alice@corp.com", "bob@corp.com"}
		if len(spec.Approvers) != 2 || spec.Approvers[0] != want[0] || spec.Approvers[1] != want[1] {
			t.Errorf("approvers = %v, want %v (lowercased, trimmed, blanks dropped)", spec.Approvers, want)
		}
	})

	t.Run("falls back to default_approvers", func(t *testing.T) {
		spec := resolveDeferSpec(types.DeferConfig{DefaultApprovers: []string{"OnCall@corp.com"}},
			types.DeferToolConfig{}, hctx)
		if len(spec.Approvers) != 1 || spec.Approvers[0] != "oncall@corp.com" {
			t.Errorf("approvers = %v, want [oncall@corp.com]", spec.Approvers)
		}
	})

	t.Run("empty → no allowlist", func(t *testing.T) {
		if spec := resolveDeferSpec(types.DeferConfig{}, types.DeferToolConfig{}, hctx); spec.Approvers != nil {
			t.Errorf("no approvers → nil, got %v", spec.Approvers)
		}
	})
}
