package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/initializ/guardrails"
	"github.com/initializ/guardrails/models"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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

// LibraryGuardrailEngine implements coreruntime.GuardrailChecker using the
// github.com/initializ/guardrails library. It supports two modes:
//   - File mode: uses StructuredGuardrails loaded from guardrails.json
//   - DB mode: loads config from MongoDB (set via FORGE_GUARDRAILS_DB env)
//
// On every mask/block decision the engine emits a guardrail_check audit
// event through auditLogger (when wired). The auditCfg knob controls
// whether the offending content is captured as evidence; default off.
// See issue #155.
type LibraryGuardrailEngine struct {
	manager       *guardrails.GuardrailManager
	structured    *models.StructuredGuardrails
	enforce       bool
	useDB         bool
	agentID       string
	orgID         string
	configVersion int64
	logger        coreruntime.Logger
	auditLogger   *coreruntime.AuditLogger
	auditCfg      GuardrailAuditConfig
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

// NewDBGuardrailEngine creates a guardrail engine backed by MongoDB.
// Config is loaded from the AgentConfig collection; audit logging is enabled.
func NewDBGuardrailEngine(mongoURI, agentID, orgID string, enforce bool, logger coreruntime.Logger) (*LibraryGuardrailEngine, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		return nil, fmt.Errorf("connecting to guardrails DB: %w", err)
	}

	// Verify connectivity
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("pinging guardrails DB: %w", err)
	}

	mgr, err := guardrails.NewGuardrailManager(guardrails.Config{
		MongoClient:  client,
		DatabaseName: "Initializ",
		EnableAudit:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating guardrail manager with DB: %w", err)
	}

	return &LibraryGuardrailEngine{
		manager: mgr,
		enforce: enforce,
		useDB:   true,
		agentID: agentID,
		orgID:   orgID,
		logger:  logger,
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

// structuredIfFileMode returns the StructuredGuardrails pointer only in file
// mode. In DB mode the library loads config from MongoDB automatically.
func (e *LibraryGuardrailEngine) structuredIfFileMode() *models.StructuredGuardrails {
	if e.useDB {
		return nil
	}
	return e.structured
}

// CheckInbound validates an inbound (user) message via the library's InputGate.
func (e *LibraryGuardrailEngine) CheckInbound(ctx context.Context, msg *a2a.Message) error {
	text := coreruntime.ExtractText(msg)
	if text == "" {
		return nil
	}

	result, err := e.manager.InputGate(ctx, guardrails.InputRequest{
		Content:              text,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredIfFileMode(),
		ConfigVersion:        e.configVersion,
	})
	if err != nil {
		e.logger.Warn("guardrail input gate error", map[string]any{"error": err.Error()})
		// On library error, allow request through (fail-open)
		return nil
	}

	switch result.Decision {
	case guardrails.DecisionMask:
		if result.MaskedContent != "" {
			for i := range msg.Parts {
				if msg.Parts[i].Kind == a2a.PartKindText && msg.Parts[i].Text != "" {
					msg.Parts[i].Text = result.MaskedContent
				}
			}
			e.logger.Info("inbound guardrail redaction applied", map[string]any{
				"direction": "inbound",
			})
			// Evidence carries the post-library-mask content for mask
			// decisions — same payload the LLM actually saw downstream.
			// Stamping the pre-mask original here would defeat the very
			// mask the library produced.
			e.emitGuardrailEvent(ctx, "inbound", "", result.MaskedContent, guardrailResultMasked, result)
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			// Block decisions have no MaskedContent — the message was
			// rejected outright. Evidence carries the original so the
			// operator can see what they would have sent; redact pass
			// still runs over it on the way out.
			e.emitGuardrailEvent(ctx, "inbound", "", text, guardrailResultBlocked, result)
			return fmt.Errorf("input blocked: %s", desc)
		}
		e.logger.Warn("guardrail input violation (warn mode)", map[string]any{
			"direction": "inbound",
			"detail":    desc,
		})
		e.emitGuardrailEvent(ctx, "inbound", "", text, guardrailResultWarned, result)
	}
	return nil
}

// CheckOutbound validates an outbound (agent) message via the library's OutputGate.
// Masked content is applied in-place; blocked content returns an error only in enforce mode.
func (e *LibraryGuardrailEngine) CheckOutbound(ctx context.Context, msg *a2a.Message) error {
	for i, p := range msg.Parts {
		if p.Kind != a2a.PartKindText || p.Text == "" {
			continue
		}

		original := p.Text
		result, err := e.manager.OutputGate(ctx, guardrails.OutputRequest{
			Content:              p.Text,
			EntityID:             e.agentID,
			OrgID:                e.orgID,
			EntityType:           guardrails.EntityTypeAgent,
			StructuredGuardrails: e.structuredIfFileMode(),
			ConfigVersion:        e.configVersion,
		})
		if err != nil {
			e.logger.Warn("guardrail output gate error", map[string]any{"error": err.Error()})
			continue
		}

		switch result.Decision {
		case guardrails.DecisionMask:
			if result.MaskedContent != "" {
				msg.Parts[i].Text = result.MaskedContent
				e.logger.Warn("outbound guardrail redaction applied", map[string]any{
					"direction": "outbound",
				})
				// Evidence = post-mask content (same payload the user
				// actually received). See CheckInbound for rationale.
				e.emitGuardrailEvent(ctx, "outbound", "", result.MaskedContent, guardrailResultMasked, result)
			}
		case guardrails.DecisionBlock:
			desc := violationSummary(result)
			if e.enforce {
				e.emitGuardrailEvent(ctx, "outbound", "", original, guardrailResultBlocked, result)
				return fmt.Errorf("output blocked: %s", desc)
			}
			e.logger.Warn("guardrail output violation (warn mode)", map[string]any{
				"direction": "outbound",
				"detail":    desc,
			})
			e.emitGuardrailEvent(ctx, "outbound", "", original, guardrailResultWarned, result)
		}
	}
	return nil
}

// CheckToolOutput scans tool output text via the library's OutputGate.
// Returns the (possibly masked) text and any blocking error.
func (e *LibraryGuardrailEngine) CheckToolOutput(ctx context.Context, toolName, text string) (string, error) {
	if text == "" {
		return text, nil
	}

	result, err := e.manager.OutputGate(ctx, guardrails.OutputRequest{
		Content:              text,
		EntityID:             e.agentID,
		OrgID:                e.orgID,
		EntityType:           guardrails.EntityTypeAgent,
		StructuredGuardrails: e.structuredIfFileMode(),
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
			e.logger.Warn("guardrail redaction", map[string]any{
				"direction": "tool_output",
				"tool":      toolName,
				"detail":    "content redacted",
			})
			// Evidence = post-mask content; matches what the loop sends
			// to the LLM. See CheckInbound for rationale.
			e.emitGuardrailEvent(ctx, "tool_output", toolName, result.MaskedContent, guardrailResultMasked, result)
			return result.MaskedContent, nil
		}
	case guardrails.DecisionBlock:
		desc := violationSummary(result)
		if e.enforce {
			e.emitGuardrailEvent(ctx, "tool_output", toolName, text, guardrailResultBlocked, result)
			return "", fmt.Errorf("tool output blocked: %s", desc)
		}
		e.logger.Warn("guardrail tool output violation (warn mode)", map[string]any{
			"direction": "tool_output",
			"tool":      toolName,
			"detail":    desc,
		})
		e.emitGuardrailEvent(ctx, "tool_output", toolName, text, guardrailResultWarned, result)
	}

	return text, nil
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
