package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	deferengine "github.com/initializ/forge/forge-core/security/deferpolicy"
	"github.com/initializ/forge/forge-core/types"
)

// Managed PDP decision path (design-tool-registry §4.1). On every tool call a
// BeforeToolExec hook POSTs the proposed call to a remote Policy Decision Point
// and enforces the returned allow | defer | deny verdict, default-deny. This is
// the managed counterpart to the standalone/OSS static `security.defer.tools`
// path (registerDeferHook), which is retained UNCHANGED. Only one is active at
// a time (config-selected in runner.go); enforcement is identical for both —
// return nil to proceed, return an error to abort (loop.go), park a defer.
//
// FAIL-CLOSED (§14.5): the resolver NEVER returns allow on error — every
// transport/timeout/malformed outcome becomes a Deny verdict. It never falls
// back to the static defer map either; a silent downgrade from authorization to
// opt-in approval would be a security regression.

const defaultPDPTimeout = 3 * time.Second

// Verdict is the resolver output at the CLI enforcement layer. It carries a full
// deferengine.Spec (which includes Approvers) rather than runtime.DeferSpec
// (which does not), so a PDP defer verdict feeds the parking machinery directly.
//
// CONTRACT: a zero-value Verdict is UNDEFINED and must never be treated as a
// decision. Note DecisionAllow is the zero value of PolicyDecision, so an
// accidentally-unconstructed Verdict{} would read as allow — every Verdict must
// be constructed with an explicit Decision, and the enforcement hook's default
// arm fails closed as a backstop. Do not add a code path that returns a
// Verdict{} on an authorization boundary.
type Verdict struct {
	Decision coreruntime.PolicyDecision
	Reason   string
	Op       string
	Defer    *deferengine.Spec // set only when Decision == DecisionDefer
}

// DecisionResolver reaches an authorization verdict for a proposed tool call.
// It MUST NEVER fail open — every error is a Deny Verdict, never allow-on-error
// and never a zero-value Verdict (see the contract note above). A caching
// resolver can decorate an implementation later without touching the hook or
// loop.go.
type DecisionResolver interface {
	Resolve(ctx context.Context, hctx *coreruntime.HookContext) Verdict
}

type pdpLogger interface {
	Warn(msg string, fields map[string]any)
}

// ---- wire contract (mirrors security-next models/agent_policy_model.go) ------

type pdpRequest struct {
	Tool    string         `json:"tool"`
	Op      string         `json:"op"`
	Args    map[string]any `json:"args"`
	Caller  pdpCaller      `json:"caller"`
	Agent   string         `json:"agent"`
	Session string         `json:"session,omitempty"`
	Context map[string]any `json:"context,omitempty"`
}

type pdpCaller struct {
	Subject string `json:"subject,omitempty"`
	// EntitledAccounts is RESERVED for the relational rule (deferred): Forge has
	// no end-user subject at BeforeToolExec, so it is always null in v0.0.1.
	EntitledAccounts []string `json:"entitled_accounts,omitempty"`
}

type pdpResponse struct {
	Decision      string          `json:"decision"` // allow | defer | deny
	Reason        string          `json:"reason"`
	PolicyVersion int             `json:"policy_version"`
	DeferParams   *pdpDeferParams `json:"defer_params,omitempty"`
}

type pdpDeferParams struct {
	To        string   `json:"to"`
	Timeout   string   `json:"timeout"`
	Approvers []string `json:"approvers,omitempty"`
	Context   string   `json:"context"`
}

// pdpEnvelope unwraps security-next's ApplicationResponse{status,message,data}.
type pdpEnvelope struct {
	Data pdpResponse `json:"data"`
}

// pdpResolver POSTs each proposed tool call to the platform PDP.
type pdpResolver struct {
	endpoint    string
	token       string
	orgID       string
	workspaceID string
	agentID     string
	timeout     time.Duration
	client      *http.Client
	logger      pdpLogger
}

// BuildPDPResolver constructs the managed resolver from config + the platform
// identity env (reusing the admission env constants). The endpoint is already
// env-expanded at load (ParseForgeConfig), so startup validation saw the
// resolved value and an unset ${PDP_ENDPOINT} failed loud rather than reaching
// here empty.
func BuildPDPResolver(cfg *types.ForgeConfig, logger pdpLogger) *pdpResolver {
	pc := cfg.Security.Pdp
	return &pdpResolver{
		endpoint:    pc.Endpoint,
		token:       os.Getenv(EnvPlatformToken),
		orgID:       os.Getenv(EnvOrgID),
		workspaceID: os.Getenv(EnvWorkspaceID),
		agentID:     cfg.AgentID,
		timeout:     pc.Timeout,
		client:      &http.Client{},
		logger:      logger,
	}
}

func (p *pdpResolver) Resolve(ctx context.Context, hctx *coreruntime.HookContext) Verdict {
	// op = the registry operation name the PDP keys rules on: the part after the
	// first "__" of the runtime name "<server>__<op>". The hook only reaches here
	// for PDP-governed (namespaced) tools — see pdpGoverns; a name with no "__"
	// would not be governed and is filtered before Resolve.
	op := hctx.ToolName
	if _, after, found := strings.Cut(hctx.ToolName, "__"); found {
		op = after
	}

	// Parsed args, never a rendered string. Empty args → {}.
	args := map[string]any{}
	if strings.TrimSpace(hctx.ToolInput) != "" {
		if err := json.Unmarshal([]byte(hctx.ToolInput), &args); err != nil {
			return p.deny(op, "unparsable tool arguments: "+err.Error())
		}
	}

	reqBody := pdpRequest{
		Tool:    hctx.ToolName,
		Op:      op,
		Args:    args,
		Caller:  pdpCaller{Subject: "agent:" + p.agentID},
		Agent:   p.agentID,
		Session: hctx.TaskID,
		Context: map[string]any{},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return p.deny(op, "marshal pdp request: "+err.Error())
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultPDPTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Send with a single retry on TRANSPORT error only — a dropped packet /
	// reset shouldn't deny an otherwise-legitimate call. NOT retried: a 200
	// deny (a real verdict), or a timeout — the shared callCtx bounds total
	// time, so a retry after the deadline blows fails instantly and denies.
	//
	// NOTE (data flow): the PARSED tool arguments are sent to the PDP in full
	// (it decides over argument values), so for tools whose args carry secrets
	// or PII those values leave the pod. Keep the endpoint cluster-internal and
	// TLS-terminated at any boundary it crosses; redact sensitive fields before
	// they reach the decision point if needed.
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.endpoint, bytes.NewReader(body))
		if err != nil {
			return p.deny(op, "build pdp request: "+err.Error())
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.token)
		// Tenancy headers: the platform verifies a per-org HS256 token and needs
		// Org-Id to select the signing secret before validating the bearer.
		if p.orgID != "" {
			req.Header.Set("Org-Id", p.orgID)
		}
		if p.workspaceID != "" {
			req.Header.Set("Workspace-Id", p.workspaceID)
		}

		var doErr error
		resp, doErr = p.client.Do(req)
		if doErr == nil {
			break
		}
		// Out of attempts, or the deadline is blown (a retry would fail
		// instantly) → fail closed.
		if attempt == 1 || callCtx.Err() != nil {
			return p.deny(op, "pdp unreachable: "+doErr.Error())
		}
	}
	if resp == nil { // defensive — the loop always sets resp or denies
		return p.deny(op, "pdp unreachable: no response")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return p.deny(op, fmt.Sprintf("pdp returned HTTP %d", resp.StatusCode))
	}

	var env pdpEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return p.deny(op, "parse pdp response: "+err.Error())
	}
	pr := env.Data

	switch pr.Decision {
	case "allow":
		return Verdict{Decision: coreruntime.DecisionAllow, Reason: pr.Reason, Op: op}
	case "deny":
		reason := pr.Reason
		if reason == "" {
			reason = "denied by policy"
		}
		return Verdict{Decision: coreruntime.DecisionDeny, Reason: reason, Op: op}
	case "defer":
		if pr.DeferParams == nil {
			return p.deny(op, "pdp returned defer with no defer_params")
		}
		spec := deferengine.Spec{
			To:                 pr.DeferParams.To,
			Timeout:            parsePDPTimeout(pr.DeferParams.Timeout),
			ContextForApprover: pr.DeferParams.Context,
			Approvers:          normalizeApprovers(pr.DeferParams.Approvers),
		}
		return Verdict{Decision: coreruntime.DecisionDefer, Reason: pr.Reason, Op: op, Defer: &spec}
	default:
		return p.deny(op, fmt.Sprintf("pdp returned unknown decision %q", pr.Decision))
	}
}

// deny is the single fail-closed exit — every error path routes here.
func (p *pdpResolver) deny(op, reason string) Verdict {
	if p.logger != nil {
		p.logger.Warn("pdp: failing closed to deny", map[string]any{"op": op, "reason": reason})
	}
	return Verdict{Decision: coreruntime.DecisionDeny, Reason: reason, Op: op}
}

func parsePDPTimeout(s string) time.Duration {
	if s == "" {
		return 0 // engine applies its 10m default
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// registerPDPDecisionHook wires the managed PDP path as a single BeforeToolExec
// hook that fires on EVERY tool call. Allow → proceed; Deny → abort; Defer →
// the existing parking machinery (shared park()).
// pdpGoverns reports whether the PDP is in scope for a tool call. The PDP
// governs only NAMESPACED registry operations — "<server>__<op>" materialized
// from apis.servers / mcp.servers (forge's registry gate RESERVES "__" for that
// namespacing, so a builtin or script tool never contains it). Built-ins
// (read_skill, datetime_now, web_fetch, …) and skill script tools are NOT
// registry-admitted and can't be expressed as a PDP rule, so they must bypass
// the PDP: consulting it makes security-next default-deny ("op is not governed
// by any admitted registry tool"), which would brick the agent (it couldn't
// even read_skill to load its skill). Those tools stay governed by egress + the
// platform deny-list policy, not the PDP.
func pdpGoverns(toolName string) bool {
	return strings.Contains(toolName, "__")
}

func (r *Runner) registerPDPDecisionHook(hooks *coreruntime.HookRegistry, resolver DecisionResolver, store TaskStatusStore, auditLogger *coreruntime.AuditLogger) {
	hooks.Register(coreruntime.BeforeToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		if !pdpGoverns(hctx.ToolName) {
			return nil // not a PDP-governed tool — no decision, no audit
		}
		v := resolver.Resolve(ctx, hctx)
		emitPDPDecision(ctx, auditLogger, hctx, r.cfg.Config.AgentID, v)

		switch v.Decision {
		case coreruntime.DecisionAllow:
			return nil
		case coreruntime.DecisionDefer:
			if v.Defer == nil {
				return fmt.Errorf("pdp: defer verdict with no defer params (fail-closed)")
			}
			return r.park(ctx, hctx, *v.Defer, store, auditLogger)
		case coreruntime.DecisionDeny:
			return fmt.Errorf("denied by policy: %s", v.Reason)
		default:
			// Belt-and-suspenders: any unexpected decision fails closed.
			return fmt.Errorf("pdp: unenforceable decision (fail-closed)")
		}
	})
}

// emitPDPDecision writes the agent-side pdp_decision audit record for every
// verdict (a deny is a permanent-log deliverable). Defer RESOLUTION is
// separately recorded by park()'s existing task_deferred* events.
func emitPDPDecision(ctx context.Context, auditLogger *coreruntime.AuditLogger, hctx *coreruntime.HookContext, agentID string, v Verdict) {
	if auditLogger == nil {
		return
	}
	auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
		Event:         coreruntime.AuditPDPDecision,
		CorrelationID: hctx.CorrelationID,
		TaskID:        hctx.TaskID,
		Fields: map[string]any{
			"tool":     hctx.ToolName,
			"op":       v.Op,
			"decision": v.Decision.String(),
			"reason":   truncateForAudit(v.Reason, 512),
			"caller":   "agent:" + agentID,
			"agent":    agentID,
		},
	})
}
