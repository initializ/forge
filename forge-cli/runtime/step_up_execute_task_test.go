package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security/stepup"
)

// fakeStepUpExecutor is the minimal AgentExecutor that reproduces the
// BeforeToolExec hook's behavior for step-up: return an error that
// wraps *stepup.RequiredError (the runtime's loop.go wraps hook
// errors as "before tool exec hook: %w"). Only the Execute method
// matters here — we're testing the executeTask → handler seam.
type fakeStepUpExecutor struct {
	err error
}

func (f *fakeStepUpExecutor) Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (*a2a.Message, error) {
	return nil, f.err
}

func (f *fakeStepUpExecutor) ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error) {
	// Streaming path isn't exercised by executeTask; a nil channel
	// with a nil error is fine for the compilation contract.
	return nil, nil
}

func (f *fakeStepUpExecutor) Close() error { return nil }

// TestExecuteTask_ReturnsStepUpError pins the #247 blocker fix: a
// step-up-required error from the executor must propagate through
// executeTask as the third return value, so the REST handler's
// `if err != nil { WriteStepUpChallengeOnError(w, err) }` branch is
// actually reachable.
//
// Pre-fix behavior: executeTask marked the task Failed, stored it,
// emitted session_end, and returned (task, snap, nil). The handler
// wrote HTTP 200 with a failed-task body — no WWW-Authenticate
// header, no way for the client to know it should step up.
//
// Post-fix: executeTask still runs the failed-task side effects
// (store + audit + invocation_complete) but now also returns the
// step-up error so the handler emits the RFC 9470 401 challenge.
func TestExecuteTask_ReturnsStepUpError(t *testing.T) {
	requiredErr := &stepup.RequiredError{
		Tool:         "cli_execute",
		RequiredAcr:  "acr:mfa",
		PresentedAcr: "",
		Reason:       "no acr claim presented",
	}
	// loop.go wraps hook errors with fmt.Errorf("before tool exec hook: %w", err).
	// Reproduce that here so we're testing the errors.As-unwrap path
	// the writer relies on.
	wrappedErr := fmt.Errorf("before tool exec hook: %w", requiredErr)

	r := &Runner{
		logger:         nopLogger{},
		cancelRegistry: coreruntime.NewCancellationRegistry(),
	}
	store := a2a.NewTaskStore()
	executor := &fakeStepUpExecutor{err: wrappedErr}
	guardrails := &coreruntime.NoopGuardrailChecker{}
	auditBuf := &bytes.Buffer{}
	auditLogger := coreruntime.NewAuditLogger(auditBuf)

	params := a2a.SendTaskParams{
		ID: "task-stepup-e2e",
		Message: a2a.Message{
			Role:  a2a.MessageRoleUser,
			Parts: []a2a.Part{a2a.NewTextPart("run a cli command")},
		},
	}

	task, _, err := r.executeTask(context.Background(), params, store, executor, guardrails, http.DefaultClient, auditLogger)

	// [1] The error must propagate — pre-fix this was always nil.
	if err == nil {
		t.Fatal("executeTask returned err=nil for a step-up error — REST handler will never reach WriteStepUpChallengeOnError")
	}
	// [2] The error must be errors.As-unwrappable to *RequiredError
	// so the writer can extract the RequiredAcr for the challenge.
	if _, ok := stepup.AsRequiredError(err); !ok {
		t.Fatalf("returned err is not a step-up RequiredError: %v", err)
	}
	// [3] Failed-task side effects still fire — the task must be
	// stored as Failed so a GET /tasks/{id} after re-auth sees the
	// prior failure.
	if task == nil || task.Status.State != a2a.TaskStateFailed {
		t.Errorf("task status: got %v want Failed", task.Status.State)
	}
	stored := store.Get(params.ID)
	if stored == nil || stored.Status.State != a2a.TaskStateFailed {
		t.Errorf("task not stored as Failed: %+v", stored)
	}
	// [4] Audit trail exists: session_end + invocation_complete.
	auditStr := auditBuf.String()
	if !strings.Contains(auditStr, `"event":"session_end"`) {
		t.Error("missing session_end audit event")
	}
}

// TestExecuteTask_NonStepUpErrorStaysAsNil pins the OTHER side of the
// contract: the fix must be surgical. Executor errors that AREN'T
// step-up (generic tool failure, guardrail deny during Execute, etc)
// keep the pre-#247 behavior — task marked Failed, err returned as
// nil, handler renders a 200 with the failed-task body. Otherwise
// every tool failure would leak up as a top-level HTTP error and
// break existing client expectations.
func TestExecuteTask_NonStepUpErrorStaysAsNil(t *testing.T) {
	genericErr := errors.New("tool blew up: some other reason")

	r := &Runner{
		logger:         nopLogger{},
		cancelRegistry: coreruntime.NewCancellationRegistry(),
	}
	store := a2a.NewTaskStore()
	executor := &fakeStepUpExecutor{err: genericErr}
	guardrails := &coreruntime.NoopGuardrailChecker{}
	auditLogger := coreruntime.NewAuditLogger(&bytes.Buffer{})

	params := a2a.SendTaskParams{
		ID: "task-non-stepup",
		Message: a2a.Message{
			Role:  a2a.MessageRoleUser,
			Parts: []a2a.Part{a2a.NewTextPart("normal request")},
		},
	}

	task, _, err := r.executeTask(context.Background(), params, store, executor, guardrails, http.DefaultClient, auditLogger)
	if err != nil {
		t.Fatalf("non-step-up executor error should NOT bubble as HTTP err — got %v", err)
	}
	if task.Status.State != a2a.TaskStateFailed {
		t.Errorf("expected task Failed, got %v", task.Status.State)
	}
}

// TestExecuteTask_StepUpEndToEnd_ChallengeEmitted closes the loop
// Manoj asked for: driving the REST handler end-to-end with a
// step-up-triggering executor must emit HTTP 401 with the RFC 9470
// WWW-Authenticate challenge, not 200 with a failed-task body.
//
// Rather than wire the whole REST handler (which needs a full
// server.Server), we assert the seam the handler owns: `if err !=
// nil { WriteStepUpChallengeOnError(w, err) }` is now reachable
// because executeTask returns err.
func TestExecuteTask_StepUpEndToEnd_ChallengeEmitted(t *testing.T) {
	requiredErr := &stepup.RequiredError{
		Tool:         "cli_execute",
		RequiredAcr:  "acr:hardware",
		PresentedAcr: "acr:password",
		Reason:       `presented acr "acr:password" does not satisfy required "acr:hardware"`,
	}
	wrapped := fmt.Errorf("before tool exec hook: %w", requiredErr)

	r := &Runner{
		logger:         nopLogger{},
		cancelRegistry: coreruntime.NewCancellationRegistry(),
	}
	params := a2a.SendTaskParams{
		ID: "task-e2e",
		Message: a2a.Message{
			Role:  a2a.MessageRoleUser,
			Parts: []a2a.Part{a2a.NewTextPart("do the thing")},
		},
	}

	// Simulate the REST handler shape exactly.
	rec := httptest.NewRecorder()
	_, _, err := r.executeTask(context.Background(), params,
		a2a.NewTaskStore(),
		&fakeStepUpExecutor{err: wrapped},
		&coreruntime.NoopGuardrailChecker{},
		http.DefaultClient,
		coreruntime.NewAuditLogger(&bytes.Buffer{}))

	if err == nil {
		t.Fatal("executeTask err was nil — the REST handler's `if err != nil` branch would not fire and the 401 challenge would never be emitted (the pre-fix bug Manoj flagged)")
	}
	// This is the handler's actual code path:
	//   if WriteStepUpChallengeOnError(w, err) { return }
	if !WriteStepUpChallengeOnError(rec, err) {
		t.Fatal("WriteStepUpChallengeOnError returned false — errors.As unwrap through the executor wrap failed")
	}

	if rec.Code != 401 {
		t.Errorf("status: got %d want 401", rec.Code)
	}
	wwa := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(wwa, `error="step_up_required"`) {
		t.Errorf(`WWW-Authenticate missing step_up_required token: %q`, wwa)
	}
	if !strings.Contains(wwa, `acr_values="acr:hardware"`) {
		t.Errorf(`WWW-Authenticate missing acr_values="acr:hardware": %q`, wwa)
	}
	var body map[string]any
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatalf("body isn't JSON: %v — %s", e, rec.Body.String())
	}
	if body["error"] != "step_up_required" {
		t.Errorf("body error field: %v", body["error"])
	}
	if body["required_acr"] != "acr:hardware" {
		t.Errorf("body required_acr: %v", body["required_acr"])
	}
	if body["tool"] != "cli_execute" {
		t.Errorf("body tool: %v", body["tool"])
	}
}
