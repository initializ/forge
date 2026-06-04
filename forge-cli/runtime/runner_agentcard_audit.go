package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// emitAgentCardPublished writes the agent_card_published audit event
// described by issue #85 / FWS-1. The event carries the card's identity
// + size metadata + a sha256 of the JSON-encoded card so consumers can
// detect config drift across deploys. Full payload bytes are
// intentionally NOT emitted — sizes and the hash are enough for an
// audit trail and keep the event under the size budget every other
// Forge event respects.
//
// The emit is best-effort: serialization or audit failures log a
// warning and otherwise let the agent continue. Failing to emit an
// audit event must never block startup.
func (r *Runner) emitAgentCardPublished(audit *coreruntime.AuditLogger, card *a2a.AgentCard) {
	if audit == nil || card == nil {
		return
	}

	raw, err := json.Marshal(card)
	if err != nil {
		r.logger.Warn("agent_card_published: marshal failed", map[string]any{"error": err.Error()})
		return
	}
	sum := sha256.Sum256(raw)

	// Collect security scheme names (not their full definitions —
	// downstream consumers only need to know which schemes are
	// advertised, not the OIDC URLs or bearer formats).
	schemeNames := make([]string, 0, len(card.SecuritySchemes))
	for name := range card.SecuritySchemes {
		schemeNames = append(schemeNames, name)
	}

	caps := map[string]bool{}
	if card.Capabilities != nil {
		caps["streaming"] = card.Capabilities.Streaming
		caps["push_notifications"] = card.Capabilities.PushNotifications
		caps["state_transition_history"] = card.Capabilities.StateTransitionHistory
	}

	audit.Emit(coreruntime.AuditEvent{
		Event: coreruntime.EventAgentCardPublished,
		Fields: map[string]any{
			"name":             card.Name,
			"version":          card.Version,
			"protocol_version": card.ProtocolVersion,
			"url":              card.URL,
			"skill_count":      len(card.Skills),
			"capabilities":     caps,
			"security_schemes": schemeNames,
			"card_size_bytes":  len(raw),
			"card_sha256":      hex.EncodeToString(sum[:]),
		},
	})
}
