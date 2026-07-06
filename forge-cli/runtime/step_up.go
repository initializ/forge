package runtime

import (
	"context"
	"fmt"
	"net/http"

	"github.com/initializ/forge/forge-core/auth"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security/stepup"
)

// buildStepUpEngine constructs the R4b step-up engine from
// operator config. Returns (nil, nil) when the check is disabled —
// the runner then skips hook registration and challenge handling.
//
// Fail-loud on config errors: an operator who declared
// step_up.enabled=true with no tools listed gets a startup failure.
func (r *Runner) buildStepUpEngine() (*stepup.Engine, error) {
	cfg := r.cfg.Config.Security.StepUp
	if !cfg.Enabled {
		return nil, nil
	}
	return stepup.New(stepup.Config{
		Enabled:      true,
		Tools:        cfg.Tools,
		AcrHierarchy: cfg.AcrHierarchy,
	})
}

// registerStepUpHook wires the BeforeToolExec hook that enforces
// the R4b step-up requirement. The hook:
//
//  1. Looks up the acr requirement for the tool (no-op when the tool
//     has no requirement declared).
//  2. Reads the caller's Identity from ctx (populated by the auth
//     middleware at the request boundary).
//  3. Calls engine.Check; on step-up-required, emits the audit event
//     and returns the typed *stepup.RequiredError.
//  4. The A2A response layer catches the typed error via
//     WriteStepUpChallengeOnError (below) and translates into an
//     RFC 9470 401 response.
//
// Ordering matters: this hook fires AFTER guardrails so a caller
// whose input the guardrail would reject anyway doesn't see the
// step-up challenge unnecessarily. But it fires BEFORE the tool
// executes — that's the whole point.
func (r *Runner) registerStepUpHook(hooks *coreruntime.HookRegistry, auditLogger *coreruntime.AuditLogger) {
	if r.stepUpEngine == nil || !r.stepUpEngine.Enabled() {
		return
	}
	engine := r.stepUpEngine
	hooks.Register(coreruntime.BeforeToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		requiredAcr := engine.RequirementFor(hctx.ToolName)
		if requiredAcr == "" {
			return nil // fast path: tool has no requirement
		}
		identity := auth.IdentityFromContext(ctx)
		if err := engine.Check(hctx.ToolName, identity); err != nil {
			re, _ := stepup.AsRequiredError(err)
			// Emit the audit event before returning so the SIEM
			// records the step-up even if the caller never retries.
			fields := map[string]any{
				"tool":         hctx.ToolName,
				"required_acr": re.RequiredAcr,
				"reason":       re.Reason,
			}
			if re.PresentedAcr != "" {
				fields["presented_acr"] = re.PresentedAcr
			}
			auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
				Event:         coreruntime.AuditAuthStepUpRequired,
				CorrelationID: hctx.CorrelationID,
				TaskID:        hctx.TaskID,
				Fields:        fields,
			})
			return err
		}
		return nil
	})
}

// WriteStepUpChallengeOnError inspects err for a *stepup.RequiredError
// and, if present, writes an RFC 9470 step-up challenge to w:
//
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Bearer error="step_up_required",
//	                         acr_values="<RequiredAcr>"
//
// Returns true when it handled the response — the caller MUST NOT
// write a second response body. Returns false when err is not a
// step-up error, letting the caller fall through to its default
// error handler.
//
// Split out so the three tasks/send handler variants (JSON-RPC,
// REST, SSE) all get the same challenge format via one code path.
func WriteStepUpChallengeOnError(w http.ResponseWriter, err error) bool {
	re, ok := stepup.AsRequiredError(err)
	if !ok {
		return false
	}
	// RFC 9470 format: quote the values, comma-separate params.
	// error="step_up_required" is the standard token; acr_values
	// carries the required acr so the client can enroll the right
	// authentication method on the retry.
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer error="step_up_required", acr_values=%q`, re.RequiredAcr))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	// Body carries the same shape as the audit event so a caller
	// debugging the challenge can grep for the exact tool + acr
	// pair.
	_, _ = fmt.Fprintf(w,
		`{"error":"step_up_required","tool":%q,"required_acr":%q,"reason":%q}`,
		re.Tool, re.RequiredAcr, re.Reason)
	return true
}
