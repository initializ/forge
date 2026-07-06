package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/initializ/forge/forge-cli/server"
	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	deferengine "github.com/initializ/forge/forge-core/security/deferpolicy"
	"github.com/initializ/forge/forge-core/types"
)

// registerDeferHook wires the R4c (#211) BeforeToolExec hook that
// pauses the executor when a tool is listed in `security.defer.tools`.
//
// Pause mechanism: the hook goroutine (which is holding the HTTP
// request open in the tasks/send path) blocks on Handle.WaitCtx.
// While blocked:
//   - The task's Status in the store flips to `deferred` so
//     parallel `GET /tasks/{id}` requests see the deferred state.
//   - A `task_deferred` audit event is emitted.
//   - The timeout timer runs in the background.
//
// When the deferral resolves (via POST /tasks/{id}/decisions or the
// timeout):
//   - approve → hook returns nil; the tool proceeds; task status
//     flips back to `working`.
//   - reject → hook returns an error; the tool fails with a
//     defer-denied message; task ends `failed`.
//   - timeout → same as reject with a distinct audit event.
//
// SetStatus lets *Runner satisfy TaskStatusStore so the defer hook
// can flip the task's Status via the runner even before the a2a
// server (which owns the store) has been constructed. Resolves
// r.taskStore lazily — nil-safe if the runner is being used
// outside a real server (unit tests).
func (r *Runner) SetStatus(id string, s a2a.TaskStatus) a2a.TaskStatus {
	if r.taskStore == nil {
		return a2a.TaskStatus{}
	}
	prev := r.taskStore.Get(id)
	var prevStatus a2a.TaskStatus
	if prev != nil {
		prevStatus = prev.Status
	}
	r.taskStore.UpdateStatus(id, s)
	return prevStatus
}

func (r *Runner) registerDeferHook(hooks *coreruntime.HookRegistry, store TaskStatusStore, auditLogger *coreruntime.AuditLogger) {
	cfg := r.cfg.Config.Security.Defer
	if !cfg.Enabled || len(cfg.Tools) == 0 {
		return
	}
	engine := r.deferEngine
	if engine == nil {
		return
	}
	hooks.Register(coreruntime.BeforeToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		toolCfg, has := cfg.Tools[hctx.ToolName]
		if !has {
			return nil // fast path: tool has no defer requirement
		}

		spec := resolveDeferSpec(cfg, toolCfg, hctx)
		handle, err := engine.Register(hctx.TaskID, hctx.ToolName, spec)
		if err != nil {
			// Only happens on duplicate register — a hook-level bug.
			// Fail closed rather than silently dropping.
			return fmt.Errorf("defer: %w", err)
		}

		// Flip task status → deferred while we wait so parallel
		// GET /tasks/{id} readers see the deferred state.
		originalStatus := store.SetStatus(hctx.TaskID, a2a.TaskStatus{
			State: a2a.TaskStateDeferred,
			Message: &a2a.Message{
				Role: a2a.MessageRoleAgent,
				Parts: []a2a.Part{
					a2a.NewTextPart(fmt.Sprintf(
						"Deferred: awaiting %s decision on %s",
						spec.To, hctx.ToolName)),
				},
			},
		})

		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditTaskDeferred,
			CorrelationID: hctx.CorrelationID,
			TaskID:        hctx.TaskID,
			Fields: map[string]any{
				"tool":       hctx.ToolName,
				"to":         spec.To,
				"timeout_ms": spec.Timeout.Milliseconds(),
				"context":    truncateForAudit(spec.ContextForApprover, 512),
			},
		})

		start := time.Now()
		res, waitErr := handle.WaitCtx(ctx)
		if waitErr != nil {
			// ctx cancelled — the caller abandoned the request. Roll
			// back task status and clean up the pending deferral.
			// engine.Register left the timer running; resolve with
			// timeout so the timer fires immediately if it hasn't
			// already.
			store.SetStatus(hctx.TaskID, originalStatus)
			_ = engine.Resolve(hctx.TaskID, deferengine.Resolution{Decision: deferengine.DecisionTimeout})
			return waitErr
		}

		waitMs := time.Since(start).Milliseconds()

		switch res.Decision {
		case deferengine.DecisionApprove:
			// Approve: restore working state, let the tool proceed.
			store.SetStatus(hctx.TaskID, originalStatus)
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditTaskDeferredDecision,
				CorrelationID: hctx.CorrelationID,
				TaskID:        hctx.TaskID,
				Fields: map[string]any{
					"tool":     hctx.ToolName,
					"decision": string(res.Decision),
					"approver": res.Approver,
					"note":     res.Note,
					"wait_ms":  waitMs,
				},
			})
			return nil
		case deferengine.DecisionReject:
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditTaskDeferredDecision,
				CorrelationID: hctx.CorrelationID,
				TaskID:        hctx.TaskID,
				Fields: map[string]any{
					"tool":     hctx.ToolName,
					"decision": string(res.Decision),
					"approver": res.Approver,
					"note":     res.Note,
					"wait_ms":  waitMs,
				},
			})
			return fmt.Errorf("defer: rejected by %s: %s", res.Approver, res.Note)
		case deferengine.DecisionTimeout:
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditTaskDeferredTimeout,
				CorrelationID: hctx.CorrelationID,
				TaskID:        hctx.TaskID,
				Fields: map[string]any{
					"tool":       hctx.ToolName,
					"timeout_ms": spec.Timeout.Milliseconds(),
					"wait_ms":    waitMs,
				},
			})
			return fmt.Errorf("defer: no decision within %s (auto-deny)", spec.Timeout)
		default:
			return fmt.Errorf("defer: unknown decision %q", res.Decision)
		}
	})
}

// resolveDeferSpec composes a Spec from the tool-level + top-level
// config, applying defaults.
func resolveDeferSpec(cfg types.DeferConfig, tool types.DeferToolConfig, hctx *coreruntime.HookContext) deferengine.Spec {
	to := tool.To
	if to == "" {
		to = cfg.DefaultTo
	}
	timeout := tool.Timeout
	if timeout == 0 {
		timeout = cfg.DefaultTimeout
	}
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctxTemplate := tool.ContextTemplate
	if ctxTemplate == "" {
		ctxTemplate = "tool={tool} args={args}"
	}
	// Small template substitution: {tool} and {args}. No full
	// templating engine — keeps the audit event predictable.
	rendered := strings.NewReplacer(
		"{tool}", hctx.ToolName,
		"{args}", hctx.ToolInput,
	).Replace(ctxTemplate)
	return deferengine.Spec{
		To:                 to,
		Timeout:            timeout,
		ContextForApprover: rendered,
	}
}

// truncateForAudit caps context strings on the audit event so a
// large tool-input JSON doesn't blow the sink budget. Slices runes,
// not bytes — same reason as R9's truncate.
func truncateForAudit(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// TaskStatusStore is the narrow interface the defer hook uses to
// flip task status while blocked. The runner's a2a task store
// satisfies this; the interface exists so the hook doesn't take a
// hard dep on the full server.TaskStore surface (which would drag
// heavy imports into forge-cli/runtime).
type TaskStatusStore interface {
	// SetStatus replaces the status of task `id`. Returns the
	// previous status so callers can restore it after a resolve.
	SetStatus(id string, s a2a.TaskStatus) a2a.TaskStatus
}

// registerDecisionsEndpoint wires POST /tasks/{id}/decisions so
// external approvers (webhook from Slack, human ops UI) can
// resolve a pending deferral. Returns 404 for unknown tasks,
// 409 for tasks not in a deferred state, 200 with the resolution
// payload on success.
func (r *Runner) registerDecisionsEndpoint(srv *server.Server, auditLogger *coreruntime.AuditLogger) {
	if r.deferEngine == nil {
		return
	}
	srv.RegisterHTTPHandler("POST /tasks/{id}/decisions", makeDecisionsHandler(r.deferEngine))
}

// makeDecisionsHandler returns the http.HandlerFunc for
// POST /tasks/{id}/decisions. Extracted from the closure in
// registerDecisionsEndpoint so `defer_test.go` can exercise
// status-code paths (404 / 400 / 409 / 200) without spinning up
// the full a2a server.
func makeDecisionsHandler(engine *deferengine.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		taskID := req.PathValue("id")
		if taskID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing task id"})
			return
		}
		var body struct {
			Decision string `json:"decision"`
			Approver string `json:"approver"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
			return
		}
		decision := deferengine.Decision(strings.ToLower(strings.TrimSpace(body.Decision)))
		if decision != deferengine.DecisionApprove && decision != deferengine.DecisionReject {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": `decision must be "approve" or "reject"`,
			})
			return
		}
		if _, ok := engine.Peek(taskID); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "no pending deferral for task",
			})
			return
		}
		if err := engine.Resolve(taskID, deferengine.Resolution{
			Decision: decision,
			Approver: body.Approver,
			Note:     body.Note,
		}); err != nil {
			// Race: another decision landed in the window between
			// Peek and Resolve. 409 is the honest signal.
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":  taskID,
			"decision": string(decision),
			"approver": body.Approver,
		})
	}
}
