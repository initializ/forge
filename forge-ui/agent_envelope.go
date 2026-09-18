package forgeui

import (
	"encoding/json"
	"strings"
)

// agentEnvelope is the structured output the AI agent-builder LLM
// returns each turn, mirroring the skill builder's {message, skill}
// contract (skill_envelope.go). `message` is the human-facing chat text
// (a clarifying question while interviewing, or a summary once a draft
// is ready). `agent` is null while the interview is still converging and
// carries the full draft the moment the agent becomes buildable.
type agentEnvelope struct {
	Message string      `json:"message"`
	Agent   *AgentDraft `json:"agent"`
}

// parseAgentEnvelope extracts the {message, agent} envelope from a raw
// LLM response. It reuses the skill builder's brace-scanning helper
// (jsonObjectAt) and is deliberately forgiving, applying the same
// hardening parseSkillEnvelope does:
//
//  1. It tries EVERY balanced {...} candidate in order, so an envelope
//     preceded by prose that itself contains JSON isn't missed.
//  2. It requires both the "message" and "agent" keys (looksLikeAgentEnvelope)
//     before committing, so an incidental {"message":"ok"} in prose can't
//     hijack the parse.
//  3. It skips a zero-value match (no message, no agent) so a wrapper
//     object like {"response": {...}} doesn't win over the inner envelope.
//
// When no structured envelope is found, message is the raw response and
// agent is nil (structured=false) — the model is treated as still
// interviewing, never as having produced a blank draft.
func parseAgentEnvelope(response string) (message string, agent *AgentDraft, structured bool) {
	for from := 0; from < len(response); {
		cand, start := jsonObjectAt(response, from)
		if cand == "" {
			break
		}
		next := start + 1
		if !looksLikeAgentEnvelope(cand) {
			from = next
			continue
		}
		var env agentEnvelope
		if err := json.Unmarshal([]byte(cand), &env); err != nil {
			from = next
			continue
		}
		if env.Message == "" && env.Agent == nil {
			from = next
			continue
		}
		return strings.TrimSpace(env.Message), env.Agent, true
	}
	return strings.TrimSpace(response), nil, false
}

// looksLikeAgentEnvelope guards against a false-positive structured
// parse on some OTHER JSON object in the response. It requires BOTH the
// "message" and "agent" keys — the prompt mandates exactly those two
// top-level fields, so a real envelope always carries both (even while
// interviewing, `agent` is present as null).
func looksLikeAgentEnvelope(jsonStr string) bool {
	return strings.Contains(jsonStr, `"message"`) && strings.Contains(jsonStr, `"agent"`)
}
