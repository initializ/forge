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
	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/mcp"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security/authgate"
)

// ConsentDeliverer delivers an MCP auth-required consent prompt to the
// requesting user (e.g. a Slack DM with a "Connect Atlassian" link, or an
// A2A `auth-required` artifact). It is the auth-gate analog of
// DeferralNotifier.
//
// Mode split (design-tool-registry.md §18.4): in MANAGED mode the platform
// owns delivery + the consent callback + token custody, so Forge is handed
// a deliverer that hands off to the platform. In STANDALONE mode the default
// is nil (no delivery yet) until the loopback resolver lands (#330 inc 4);
// the gate still parks and the resume endpoint still works, so an operator
// or the platform can drive consent out-of-band.
//
// Best-effort: a delivery error is logged, never fatal — the parked call
// still resumes when a grant arrives via the resume endpoint, and blocking
// on a channel outage would be strictly worse.
type ConsentDeliverer func(ctx context.Context, subject, server, taskID string, deadline time.Time) error

// mcpAuthGate is the runtime implementation of adapters.AuthGate (#330). It
// bridges the pure authgate.Engine to the runtime concerns the engine
// deliberately doesn't know about: identity extraction, task-status flips,
// consent delivery, and audit.
type mcpAuthGate struct {
	engine    *authgate.Engine
	store     TaskStatusStore // flips task status → auth-required while parked
	audit     *coreruntime.AuditLogger
	deliverer ConsentDeliverer // optional; nil ⇒ no prompt delivery (park + resume still work)
	logger    coreruntime.Logger
}

// Await parks the executor because `server` has no grant for the requesting
// user in ctx, resuming once consent lands (→ nil, the caller re-resolves)
// or the wait ends without it (→ error wrapping mcp.ErrNoToken, so the tool
// adapter classifies it as `no_token` — the same reason code as the failure
// it replaced).
func (g *mcpAuthGate) Await(ctx context.Context, server string) error {
	subject := delegatedSubject(ctx)
	if subject == "" {
		// No requesting user ⇒ a gate can't be addressed to anyone. Fail
		// through as auth-required rather than parking a nameless call.
		return fmt.Errorf("%w: no requesting user in context to consent for %q", mcp.ErrNoToken, server)
	}
	taskID := coreruntime.TaskIDFromContext(ctx)
	correlationID := coreruntime.CorrelationIDFromContext(ctx)

	handle, first, err := g.engine.Await(subject, server, authgate.Spec{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("%w: %v", mcp.ErrNoToken, err)
	}

	// Flip THIS call's task → auth-required so parallel GET /tasks/{id}
	// readers see it's blocked on consent, not hung. Every parked caller
	// flips its own task; only the gate creator (first) delivers a prompt,
	// so one user gets one prompt no matter how many calls pile on.
	originalStatus := g.setAuthRequired(taskID, subject, server)

	if first {
		g.emit(ctx, coreruntime.EventMCPAuthRequired, correlationID, taskID, map[string]any{
			"server":     server,
			"subject":    subject,
			"deadline":   handle.Deadline().UTC().Format(time.RFC3339),
			"timeout_ms": time.Until(handle.Deadline()).Milliseconds(),
		})
		g.deliver(ctx, subject, server, taskID, handle.Deadline())
	}

	start := time.Now()
	res, waitErr := handle.WaitCtx(ctx)
	if waitErr != nil {
		// ctx cancelled — caller abandoned the request. Restore status; the
		// engine tears the gate down when the last waiter leaves.
		g.restore(taskID, originalStatus)
		return waitErr
	}
	waitMs := time.Since(start).Milliseconds()

	switch res.Decision {
	case authgate.DecisionGranted:
		g.restore(taskID, originalStatus)
		g.emit(ctx, coreruntime.EventMCPAuthResolved, correlationID, taskID, map[string]any{
			"server": server, "subject": subject, "wait_ms": waitMs,
		})
		return nil
	default: // DecisionTimeout / DecisionCanceled
		g.emit(ctx, coreruntime.EventMCPAuthTimeout, correlationID, taskID, map[string]any{
			"server": server, "subject": subject, "wait_ms": waitMs, "decision": string(res.Decision),
		})
		return fmt.Errorf("%w: consent for %q not granted (%s)", mcp.ErrNoToken, server, res.Decision)
	}
}

// setAuthRequired flips the task to auth-required, returning the prior status
// so Await can restore it on resume. nil store (unit tests) ⇒ no-op.
func (g *mcpAuthGate) setAuthRequired(taskID, subject, server string) a2a.TaskStatus {
	if g.store == nil || taskID == "" {
		return a2a.TaskStatus{}
	}
	return g.store.SetStatus(taskID, a2a.TaskStatus{
		State: a2a.TaskStateAuthRequired,
		Message: &a2a.Message{
			Role: a2a.MessageRoleAgent,
			Parts: []a2a.Part{
				a2a.NewTextPart(fmt.Sprintf(
					"Authorization required: connect %s to continue (as %s)", server, subject)),
			},
		},
	})
}

func (g *mcpAuthGate) restore(taskID string, prev a2a.TaskStatus) {
	if g.store == nil || taskID == "" {
		return
	}
	g.store.SetStatus(taskID, prev)
}

func (g *mcpAuthGate) deliver(ctx context.Context, subject, server, taskID string, deadline time.Time) {
	if g.deliverer == nil {
		return
	}
	if err := g.deliverer(ctx, subject, server, taskID, deadline); err != nil && g.logger != nil {
		g.logger.Warn("mcp consent delivery failed", map[string]any{
			"server": server, "subject": subject, "error": err.Error(),
		})
	}
}

func (g *mcpAuthGate) emit(ctx context.Context, event, correlationID, taskID string, fields map[string]any) {
	if g.audit == nil {
		return
	}
	g.audit.EmitFromContext(ctx, coreruntime.AuditEvent{
		Event:         event,
		CorrelationID: correlationID,
		TaskID:        taskID,
		Fields:        fields,
	})
}

// delegatedSubject extracts the consenting user from ctx: email preferred
// (stable, human-meaningful, matches the delegated token subject), else the
// opaque UserID. Mirrors mcp.delegatedSubject, duplicated here to avoid
// exporting it across the module boundary.
func delegatedSubject(ctx context.Context) string {
	id := auth.IdentityFromContext(ctx)
	if id == nil {
		return ""
	}
	if id.Email != "" {
		return id.Email
	}
	return id.UserID
}

// registerMCPConsentEndpoint wires POST /mcp/consent so the platform (managed
// mode) or an operator (standalone) can signal that a user's consent
// completed and a grant now exists — resuming every call parked on
// {subject, server}. Mirrors POST /tasks/{id}/decisions: 404 when no call is
// parked, 400 on a malformed body, 200 on resume.
//
// The endpoint carries NO token — Forge never receives the credential. It is
// a pure "a grant now exists, re-resolve" signal; the token is fetched
// through the normal delegated resolver on resume.
func (r *Runner) registerMCPConsentEndpoint(srv *server.Server, auditLogger *coreruntime.AuditLogger) {
	if r.authGateEngine == nil {
		return
	}
	srv.RegisterHTTPHandler("POST /mcp/consent", makeMCPConsentHandler(r.authGateEngine, auditLogger))
}

// makeMCPConsentHandler is extracted so tests can exercise the status-code
// paths without a full server.
func makeMCPConsentHandler(engine *authgate.Engine, auditLogger *coreruntime.AuditLogger) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Subject string `json:"subject"`
			Server  string `json:"server"`
			// Granted defaults true (the signal is "consent completed").
			// false lets the platform report a refusal so the parked call
			// fails fast instead of idling to its timeout.
			Granted *bool `json:"granted"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
			return
		}
		subject := strings.TrimSpace(body.Subject)
		serverName := strings.TrimSpace(body.Server)
		if subject == "" || serverName == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject and server are required"})
			return
		}
		if _, ok := engine.Peek(subject, serverName); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no parked call for this subject/server"})
			return
		}
		decision := authgate.DecisionGranted
		if body.Granted != nil && !*body.Granted {
			decision = authgate.DecisionTimeout // explicit refusal → fail fast
		}
		if err := engine.Resolve(subject, serverName, decision); err != nil {
			// Race: the gate resolved (timeout / another signal) between Peek
			// and Resolve. 409 is the honest signal.
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if auditLogger != nil {
			auditLogger.Emit(coreruntime.AuditEvent{
				Event: coreruntime.EventMCPAuthResolved,
				Fields: map[string]any{
					"server": serverName, "subject": subject,
					"decision": string(decision), "via": "consent_endpoint",
				},
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"subject": subject, "server": serverName, "decision": string(decision),
		})
	}
}
