package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/initializ/guardrails"
	"github.com/initializ/guardrails/models"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Result-string constants for the guardrail_check audit event. Operators
// group by these values in their SIEM pipeline; keep the set small and
// stable. Map onto library decisions:
//
//	DecisionMask  → "masked"
//	DecisionBlock (warn mode)    → "warned"
//	DecisionBlock (enforce mode) → "blocked"
const (
	guardrailResultMasked  = "masked"
	guardrailResultWarned  = "warned"
	guardrailResultBlocked = "blocked"
)

// LibraryGuardrailEngine implements coreruntime.GuardrailChecker using
// the github.com/initializ/guardrails library. Config is a
// StructuredGuardrails loaded from guardrails.json (optionally tightened by
// the platform guardrails overlay — see #284).
//
// On every mask / block / warn decision the engine emits a
// guardrail_check audit event through auditLogger (when wired). The
// fields.gate value carries the library gate type (input / context /
// tool_call / output / stream) — see issue #159 for the unified gate
// model. The auditCfg knob controls whether the offending content is
// captured as evidence (off by default — issue #155).
type LibraryGuardrailEngine struct {
	manager       *guardrails.GuardrailManager
	structured    *models.StructuredGuardrails
	enforce       bool
	agentID       string
	orgID         string
	configVersion int64
	logger        coreruntime.Logger
	auditLogger   *coreruntime.AuditLogger
	auditCfg      GuardrailAuditConfig
	// tracingCfg controls the OTel guardrail.<gate> span instrumentation
	// added in #161. CaptureContent + Redact + MaxBytes follow the
	// same posture as the #130 LLM-call content capture. Default zero
	// value disables evidence capture; the spans themselves are still
	// opened (cheap when the noop tracer is installed).
	tracingCfg observability.TracingConfig
}

// NewFileGuardrailEngine creates a guardrail engine backed by a local
// StructuredGuardrails config (loaded from guardrails.json).
func NewFileGuardrailEngine(sg *models.StructuredGuardrails, enforce bool, logger coreruntime.Logger) (*LibraryGuardrailEngine, error) {
	mgr, err := guardrails.NewGuardrailManager(guardrails.Config{})
	if err != nil {
		return nil, fmt.Errorf("creating guardrail manager: %w", err)
	}
	return &LibraryGuardrailEngine{
		manager:    mgr,
		structured: sg,
		enforce:    enforce,
		logger:     logger,
	}, nil
}

// WithAuditLogger wires an AuditLogger and capture config so the engine
// can emit guardrail_check events on every mask/block/warn decision.
// Returns the receiver for fluent construction. When auditLogger is nil
// the engine is silent on the audit pipeline (legacy behavior — only
// the ops logger sees the redaction line). Callers in the runner pass
// the same AuditLogger they hand to the A2A handlers so events share
// the configured sink stack.
func (e *LibraryGuardrailEngine) WithAuditLogger(al *coreruntime.AuditLogger, cfg GuardrailAuditConfig) *LibraryGuardrailEngine {
	e.auditLogger = al
	e.auditCfg = cfg
	return e
}

// WithTracing wires the runtime's TracingConfig so the engine can
// stamp forge.guardrail.evidence with the redact-then-truncate
// pipeline when CaptureContent is enabled. Same posture as the LLM
// call content capture from issue #130 — default off, opt-in per
// deployment, redact on by default when on. Returns the receiver for
// fluent construction.
//
// The guardrail.<gate> spans are opened unconditionally (the noop
// tracer's overhead is near-zero); CaptureContent only gates whether
// the evidence attribute is set. See issue #161.
func (e *LibraryGuardrailEngine) WithTracing(cfg observability.TracingConfig) *LibraryGuardrailEngine {
	e.tracingCfg = cfg
	return e
}

// structuredConfig returns the StructuredGuardrails the library evaluates
// each gate against.
func (e *LibraryGuardrailEngine) structuredConfig() *models.StructuredGuardrails {
	return e.structured
}

// CheckInbound validates an inbound (user) message via InputGate.
// The returned PolicyResult carries the engine's Allow/Deny/Modify
// decision (see #209). On Modify, msg.Parts is mutated in place so
// downstream reads see the redacted text.
func (e *LibraryGuardrailEngine) CheckInbound(ctx context.Context, msg *a2a.Message) (coreruntime.PolicyResult, error) {
	text := coreruntime.ExtractText(msg)
	if text == "" {
		return coreruntime.Allow(), nil
	}

	// Span lifecycle (issue #161): open before the library call so the
	// call's wall-clock duration is captured, finish in a deferred call
	// with the resolved decision + content. evidenceContent + decision
	// are mutated inside the switch below and read by the deferred
	// finish. result starts nil — finishGuardrailSpan handles that case
	// (the library-error path below sets result to nil too).
	ctx, span := startGuardrailSpan(ctx, "input", "")
	var (
		evidenceContent string
		decisionString  string
		result          *guardrails.Result
	)
	defer func() {
		finishGuardrailSpan(span, result, decisionString, evidenceContent, e.tracingCfg)
	}()

	result, err := e.manager.InputGate(ctx, guardrails.InputRequest{
		Content:              text,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredConfig(),
		ConfigVersion:        e.configVersion,
	})
	if err != nil {
		e.logger.Warn("guardrail input gate error", map[string]any{"error": err.Error()})
		return coreruntime.Allow(), nil
	}

	switch result.Decision {
	case guardrails.DecisionMask:
		if result.MaskedContent != "" {
			for i := range msg.Parts {
				if msg.Parts[i].Kind == a2a.PartKindText && msg.Parts[i].Text != "" {
					msg.Parts[i].Text = result.MaskedContent
				}
			}
			e.logger.Info("guardrail input redaction applied", nil)
			e.emitGuardrailEvent(ctx, "", result.MaskedContent, guardrailResultMasked, result)
			evidenceContent = result.MaskedContent
			decisionString = guardrailResultMasked
			return coreruntime.Modify(result.MaskedContent, violationSummary(result)), nil
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			e.emitGuardrailEvent(ctx, "", text, guardrailResultBlocked, result)
			evidenceContent = text
			decisionString = guardrailResultBlocked
			return coreruntime.Deny(desc), fmt.Errorf("input blocked: %s", desc)
		}
		e.logger.Warn("guardrail input violation (warn mode)", map[string]any{"detail": desc})
		e.emitGuardrailEvent(ctx, "", text, guardrailResultWarned, result)
		evidenceContent = text
		decisionString = guardrailResultWarned
	}
	return coreruntime.Allow(), nil
}

// CheckOutbound validates an outbound (agent) message via OutputGate.
// Masked content is applied in-place; blocked content returns an error
// only in enforce mode. One guardrail.output span per text part — the
// trace tree mirrors the part-level iteration.
//
// The aggregated PolicyResult follows the MOST-RESTRICTIVE part's
// outcome per the PolicyDecision ordering (Allow < Modify < StepUp <
// Defer < Deny). The strict `>` comparison keeps the FIRST part at
// each severity level — subsequent equal-severity parts do not
// overwrite Modified. In practice callers today only inspect the
// mutated msg.Parts (each part carries its own redaction) — the
// aggregate.Modified string is scaffolding for future R4b/R4c
// callers that need a single-string projection.
func (e *LibraryGuardrailEngine) CheckOutbound(ctx context.Context, msg *a2a.Message) (coreruntime.PolicyResult, error) {
	aggregate := coreruntime.Allow()
	for i, p := range msg.Parts {
		if p.Kind != a2a.PartKindText || p.Text == "" {
			continue
		}

		original := p.Text
		partResult, blockErr := e.checkOneOutboundPart(ctx, msg, i, original)
		if blockErr != nil {
			return partResult, blockErr
		}
		// Escalate aggregate when this part is more restrictive. Uses
		// the PolicyDecision ordinal-as-severity contract established
		// in forge-core/runtime/guardrails.go: constants are ordered
		// Allow < Modify < StepUp < Defer < Deny, so a numeric > is
		// safe here even when StepUp/Defer flow through in future.
		if partResult.Decision > aggregate.Decision {
			aggregate = partResult
		}
	}
	return aggregate, nil
}

// checkOneOutboundPart runs OutputGate over a single text part. Split
// from CheckOutbound so the per-part span has a clean lifetime (open
// at function entry, deferred close at exit) without juggling state
// across iterations.
func (e *LibraryGuardrailEngine) checkOneOutboundPart(ctx context.Context, msg *a2a.Message, i int, original string) (coreruntime.PolicyResult, error) {
	ctx, span := startGuardrailSpan(ctx, "output", "")
	var (
		evidenceContent string
		decisionString  string
		result          *guardrails.Result
	)
	defer func() {
		finishGuardrailSpan(span, result, decisionString, evidenceContent, e.tracingCfg)
	}()

	result, err := e.manager.OutputGate(ctx, guardrails.OutputRequest{
		Content:              original,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredConfig(),
		ConfigVersion:        e.configVersion,
	})
	if err != nil {
		e.logger.Warn("guardrail output gate error", map[string]any{"error": err.Error()})
		return coreruntime.Allow(), nil
	}

	switch result.Decision {
	case guardrails.DecisionMask:
		if result.MaskedContent != "" {
			msg.Parts[i].Text = result.MaskedContent
			e.logger.Warn("guardrail output redaction applied", nil)
			e.emitGuardrailEvent(ctx, "", result.MaskedContent, guardrailResultMasked, result)
			evidenceContent = result.MaskedContent
			decisionString = guardrailResultMasked
			return coreruntime.Modify(result.MaskedContent, violationSummary(result)), nil
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			e.emitGuardrailEvent(ctx, "", original, guardrailResultBlocked, result)
			evidenceContent = original
			decisionString = guardrailResultBlocked
			return coreruntime.Deny(desc), fmt.Errorf("output blocked: %s", desc)
		}
		e.logger.Warn("guardrail output violation (warn mode)", map[string]any{"detail": desc})
		e.emitGuardrailEvent(ctx, "", original, guardrailResultWarned, result)
		evidenceContent = original
		decisionString = guardrailResultWarned
	}
	return coreruntime.Allow(), nil
}

// CheckToolCall validates the arguments the agent is about to pass to
// a tool via ToolCallGate. Returns the (possibly masked) args. Wired
// from the BeforeToolExec hook in the runner.
func (e *LibraryGuardrailEngine) CheckToolCall(ctx context.Context, toolName, args string) (string, error) {
	if args == "" {
		return args, nil
	}

	ctx, span := startGuardrailSpan(ctx, "tool_call", toolName)
	var (
		evidenceContent string
		decisionString  string
		result          *guardrails.Result
	)
	defer func() {
		finishGuardrailSpan(span, result, decisionString, evidenceContent, e.tracingCfg)
	}()

	result, err := e.manager.ToolCallGate(ctx, guardrails.ToolCallRequest{
		ToolName:             toolName,
		RequestBody:          args,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredConfig(),
		ConfigVersion:        e.configVersion,
	})
	if err != nil {
		e.logger.Warn("guardrail tool_call gate error", map[string]any{
			"tool":  toolName,
			"error": err.Error(),
		})
		return args, nil
	}

	switch result.Decision {
	case guardrails.DecisionMask:
		if result.MaskedContent != "" {
			e.logger.Warn("guardrail tool_call redaction", map[string]any{"tool": toolName})
			e.emitGuardrailEvent(ctx, toolName, result.MaskedContent, guardrailResultMasked, result)
			evidenceContent = result.MaskedContent
			decisionString = guardrailResultMasked
			return result.MaskedContent, nil
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			e.emitGuardrailEvent(ctx, toolName, args, guardrailResultBlocked, result)
			evidenceContent = args
			decisionString = guardrailResultBlocked
			return "", fmt.Errorf("tool_call blocked: %s", desc)
		}
		e.logger.Warn("guardrail tool_call violation (warn mode)", map[string]any{
			"tool":   toolName,
			"detail": desc,
		})
		e.emitGuardrailEvent(ctx, toolName, args, guardrailResultWarned, result)
		evidenceContent = args
		decisionString = guardrailResultWarned
	}

	return args, nil
}

// CheckToolOutput scans tool output text via OutputGate. Returns the
// (possibly masked) text and any blocking error. The emitted event
// carries fields.tool so SIEM consumers can distinguish output-gate
// fires on tool results from output-gate fires on the model's reply
// to the user.
func (e *LibraryGuardrailEngine) CheckToolOutput(ctx context.Context, toolName, text string) (string, error) {
	if text == "" {
		return text, nil
	}

	ctx, span := startGuardrailSpan(ctx, "output", toolName)
	var (
		evidenceContent string
		decisionString  string
		result          *guardrails.Result
	)
	defer func() {
		finishGuardrailSpan(span, result, decisionString, evidenceContent, e.tracingCfg)
	}()

	result, err := e.manager.OutputGate(ctx, guardrails.OutputRequest{
		Content:              text,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredConfig(),
		ConfigVersion:        e.configVersion,
		Metadata:             map[string]interface{}{"tool_name": toolName},
	})
	if err != nil {
		e.logger.Warn("guardrail tool output gate error", map[string]any{
			"tool":  toolName,
			"error": err.Error(),
		})
		return text, nil
	}

	switch result.Decision {
	case guardrails.DecisionMask:
		if result.MaskedContent != "" {
			e.logger.Warn("guardrail tool output redaction", map[string]any{"tool": toolName})
			e.emitGuardrailEvent(ctx, toolName, result.MaskedContent, guardrailResultMasked, result)
			evidenceContent = result.MaskedContent
			decisionString = guardrailResultMasked
			return result.MaskedContent, nil
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			e.emitGuardrailEvent(ctx, toolName, text, guardrailResultBlocked, result)
			evidenceContent = text
			decisionString = guardrailResultBlocked
			return "", fmt.Errorf("tool output blocked: %s", desc)
		}
		e.logger.Warn("guardrail tool output violation (warn mode)", map[string]any{
			"tool":   toolName,
			"detail": desc,
		})
		e.emitGuardrailEvent(ctx, toolName, text, guardrailResultWarned, result)
		evidenceContent = text
		decisionString = guardrailResultWarned
	}

	return text, nil
}

// CheckContext validates retrieved context (system messages, RAG
// chunks, memory recall content) via ContextGate before it is injected
// into the LLM prompt. Returns the (possibly masked) content. Wired
// from the BeforeLLMCall hook in the runner.
func (e *LibraryGuardrailEngine) CheckContext(ctx context.Context, content string) (string, error) {
	if content == "" {
		return content, nil
	}

	ctx, span := startGuardrailSpan(ctx, "context", "")
	var (
		evidenceContent string
		decisionString  string
		result          *guardrails.Result
	)
	defer func() {
		finishGuardrailSpan(span, result, decisionString, evidenceContent, e.tracingCfg)
	}()

	result, err := e.manager.ContextGate(ctx, guardrails.ContextRequest{
		Content:              content,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredConfig(),
		ConfigVersion:        e.configVersion,
	})
	if err != nil {
		e.logger.Warn("guardrail context gate error", map[string]any{"error": err.Error()})
		return content, nil
	}

	switch result.Decision {
	case guardrails.DecisionMask:
		if result.MaskedContent != "" {
			e.logger.Warn("guardrail context redaction", nil)
			e.emitGuardrailEvent(ctx, "", result.MaskedContent, guardrailResultMasked, result)
			evidenceContent = result.MaskedContent
			decisionString = guardrailResultMasked
			return result.MaskedContent, nil
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			e.emitGuardrailEvent(ctx, "", content, guardrailResultBlocked, result)
			evidenceContent = content
			decisionString = guardrailResultBlocked
			return "", fmt.Errorf("context blocked: %s", desc)
		}
		e.logger.Warn("guardrail context violation (warn mode)", map[string]any{"detail": desc})
		e.emitGuardrailEvent(ctx, "", content, guardrailResultWarned, result)
		evidenceContent = content
		decisionString = guardrailResultWarned
	}

	return content, nil
}

// CheckStream validates a single chunk from a streaming LLM call via
// StreamGate. Returns the (possibly masked) chunk. Not auto-wired
// because Forge's current Execute loop does not call provider streaming
// (ExecuteStream buffers a single non-streaming response). Exposed for
// callers that consume llm.Client.ChatStream directly and for future
// loop work that adds a real per-chunk seam.
func (e *LibraryGuardrailEngine) CheckStream(ctx context.Context, chunk string) (string, error) {
	if chunk == "" {
		return chunk, nil
	}

	ctx, span := startGuardrailSpan(ctx, "stream", "")
	var (
		evidenceContent string
		decisionString  string
		result          *guardrails.Result
	)
	defer func() {
		finishGuardrailSpan(span, result, decisionString, evidenceContent, e.tracingCfg)
	}()

	result, err := e.manager.StreamGate(ctx, guardrails.StreamRequest{
		ChunkContent:         chunk,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredConfig(),
		ConfigVersion:        e.configVersion,
	})
	if err != nil {
		e.logger.Warn("guardrail stream gate error", map[string]any{"error": err.Error()})
		return chunk, nil
	}

	switch result.Decision {
	case guardrails.DecisionMask:
		if result.MaskedContent != "" {
			e.logger.Warn("guardrail stream redaction", nil)
			e.emitGuardrailEvent(ctx, "", result.MaskedContent, guardrailResultMasked, result)
			evidenceContent = result.MaskedContent
			decisionString = guardrailResultMasked
			return result.MaskedContent, nil
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			e.emitGuardrailEvent(ctx, "", chunk, guardrailResultBlocked, result)
			evidenceContent = chunk
			decisionString = guardrailResultBlocked
			return "", fmt.Errorf("stream blocked: %s", desc)
		}
		e.logger.Warn("guardrail stream violation (warn mode)", map[string]any{"detail": desc})
		e.emitGuardrailEvent(ctx, "", chunk, guardrailResultWarned, result)
		evidenceContent = chunk
		decisionString = guardrailResultWarned
	}

	return chunk, nil
}

// violationSummary builds a human-readable summary from result violations.
func violationSummary(r *guardrails.Result) string {
	if len(r.Violations) == 0 {
		return string(r.Decision)
	}
	var parts []string
	for _, v := range r.Violations {
		parts = append(parts, v.Description)
	}
	return strings.Join(parts, "; ")
}
