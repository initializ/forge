package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm/providers"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security/intent"
	"github.com/initializ/forge/forge-core/tools"
)

// buildIntentEngine constructs the R3 intent-alignment engine from
// the operator's forge.yaml block. Returns (nil, nil) when the check
// is disabled — the runner then skips hook registration.
//
// Fail-loud on config errors: an operator who declared
// `intent_alignment.enabled: true` but left the provider unset gets
// a startup failure, not a silent runtime bypass.
func (r *Runner) buildIntentEngine() (*intent.Engine, error) {
	cfg := r.cfg.Config.Security.IntentAlignment
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.Provider == "" {
		return nil, fmt.Errorf("intent_alignment.enabled but no provider set")
	}
	// Apply defaults where the operator left the yaml empty.
	// Thresholds are *float64 so an explicit `hard_threshold: 0`
	// (which is a legitimate configuration on cosine's [-1,1]
	// range, and the documented warn-only lever together with a
	// negative value) is preserved instead of colliding with the
	// zero-value default.
	threshold := 0.5
	if cfg.Threshold != nil {
		threshold = *cfg.Threshold
	}
	hardThreshold := 0.3
	if cfg.HardThreshold != nil {
		hardThreshold = *cfg.HardThreshold
	}
	if cfg.CacheSize == 0 {
		cfg.CacheSize = 1024
	}

	// Build the embedder. Reuses providers.NewEmbedder — same
	// dispatch as the LLM providers factory.
	apiKey := ""
	if cfg.APIKeyEnv != "" {
		apiKey = os.Getenv(cfg.APIKeyEnv)
	} else {
		// Provider defaults; providers themselves fall back further.
		switch cfg.Provider {
		case "openai":
			apiKey = os.Getenv("OPENAI_API_KEY")
		case "gemini":
			apiKey = os.Getenv("GEMINI_API_KEY")
		}
	}
	embedder, err := providers.NewEmbedder(cfg.Provider, providers.OpenAIEmbedderConfig{
		Model:   cfg.Model,
		BaseURL: cfg.BaseURL,
		APIKey:  apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("building embedder: %w", err)
	}
	return intent.New(intent.Config{
		Enabled:       true,
		Threshold:     threshold,
		HardThreshold: hardThreshold,
		CacheSize:     cfg.CacheSize,
	}, embedder)
}

// registerIntentAlignmentHook wires the BeforeToolExec hook that
// scores each tool call against the task's stated intent, emits the
// `intent_alignment` audit event, and denies on hard-threshold
// violations.
//
// Called after the LLM executor's other BeforeToolExec hooks so the
// audit event ordering is: alignment score → skill guardrail →
// runtime tool-exec log. Ordering matters for a SIEM tracing why a
// call was denied.
//
// The hook is intentionally read-only (never mutates ToolInput) — it
// is a policy decision, not a rewriter. When a warn tier fires the
// tool proceeds normally; only Deny aborts.
func (r *Runner) registerIntentAlignmentHook(hooks *coreruntime.HookRegistry, reg *tools.Registry, auditLogger *coreruntime.AuditLogger) {
	if r.intentEngine == nil || !r.intentEngine.Enabled() {
		return
	}
	engine := r.intentEngine
	hooks.Register(coreruntime.BeforeToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		// Compose the "action text" the engine embeds: tool
		// DESCRIPTION (what the tool does, per its own docstring)
		// concatenated with the LLM-supplied args JSON (specific
		// values). Description was chosen over tool name because
		// names can be arbitrary handles ("fn_42") for MCP or custom
		// tools while descriptions carry semantic meaning.
		var description string
		if reg != nil {
			if t := reg.Get(hctx.ToolName); t != nil {
				description = t.Description()
			}
		}
		if description == "" {
			// Fallback to the tool name — better than an empty
			// vector, worse than a real description. Log at debug so
			// operators can catch misconfigured tools.
			description = hctx.ToolName
			r.logger.Debug("intent_alignment: tool has no description; falling back to name", map[string]any{
				"tool": hctx.ToolName,
			})
		}
		actionText := description + "\n\nargs: " + hctx.ToolInput

		res := engine.Score(ctx, hctx.TaskID, actionText)

		fields := map[string]any{
			"tool":     hctx.ToolName,
			"decision": res.Decision.String(),
			"reason":   res.Reason,
		}
		// Encode NaN as a string on the audit wire so downstream
		// parsers don't choke on non-conforming JSON.
		if scoreJSON, jerr := json.Marshal(res.Score); jerr == nil && string(scoreJSON) != "null" {
			fields["score"] = res.Score
		} else {
			fields["score"] = "NaN"
		}
		auditLogger.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         coreruntime.AuditIntentAlignment,
			CorrelationID: hctx.CorrelationID,
			TaskID:        hctx.TaskID,
			Fields:        fields,
		})

		if res.Decision == intent.DecisionDeny {
			return fmt.Errorf("intent_alignment: %s", res.Reason)
		}
		return nil
	})
}

// CaptureStatedIntent extracts the first user-authored text part
// from an inbound A2A message and registers it with the intent
// engine. Called from the tasks/send handlers immediately after
// CheckInbound admits the message. No-op when the engine is
// disabled or the message has no text.
//
// Design note: takes the WHOLE message rather than a pre-extracted
// string so the choice of "which text counts as intent" stays here.
// Today we concatenate all text parts of the first message; a future
// refinement (structured "intent:" prefix, or an explicit A2A
// header) can change this without touching call sites.
func (r *Runner) CaptureStatedIntent(ctx context.Context, taskID string, msg *a2a.Message) {
	if r.intentEngine == nil || !r.intentEngine.Enabled() {
		return
	}
	text := coreruntime.ExtractText(msg)
	if text == "" {
		return
	}
	if err := r.intentEngine.RegisterIntent(ctx, taskID, text); err != nil {
		r.logger.Warn("intent_alignment: registering stated intent failed", map[string]any{
			"task_id": taskID,
			"error":   err.Error(),
		})
	}
}
