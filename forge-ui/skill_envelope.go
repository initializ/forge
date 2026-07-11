package forgeui

import (
	"encoding/json"
	"strings"
)

// skillEnvelope is the structured output the Skill Builder LLM returns
// each turn (issue #252 part 2), replacing the fragile quadruple-backtick
// fence format. `message` is the human-facing chat text (a clarifying
// question while interviewing, or a summary once a skill is drafted).
// `skill` is null while the interview is still converging and carries the
// full draft the moment it becomes draftable.
type skillEnvelope struct {
	Message string        `json:"message"`
	Skill   *skillPayload `json:"skill"`
}

// skillPayload is the drafted skill: the complete SKILL.md markdown plus
// any helper scripts keyed by filename. This mirrors the {skill_md,
// scripts} shape the editor/preview and the save endpoint already consume,
// so only the transport (fences -> JSON) changes, not the artifact model.
type skillPayload struct {
	SkillMD string            `json:"skill_md"`
	Scripts map[string]string `json:"scripts"`
}

// parseSkillEnvelope extracts the {message, skill} envelope from a raw LLM
// response. It is deliberately forgiving:
//
//  1. Structured path — the response is (or contains) a JSON object with a
//     `message` field. Returns structured=true. `skill` may be null (still
//     interviewing) or a full draft.
//  2. Legacy fallback — the model ignored the JSON instruction and emitted
//     the old quadruple-backtick fences. We still recover the skill via
//     extractArtifacts and surface the raw text as the message, so a model
//     that hasn't adopted the new format degrades gracefully instead of
//     showing the user nothing. Returns structured=false.
//
// message is always safe to display; skillMD is "" when no draft is present.
func parseSkillEnvelope(response string) (message, skillMD string, scripts map[string]string, structured bool) {
	scripts = make(map[string]string)

	if jsonStr := extractJSONObject(response); jsonStr != "" {
		var env skillEnvelope
		if err := json.Unmarshal([]byte(jsonStr), &env); err == nil && looksLikeEnvelope(jsonStr) {
			message = strings.TrimSpace(env.Message)
			if env.Skill != nil {
				skillMD = strings.TrimSpace(env.Skill.SkillMD)
				if env.Skill.Scripts != nil {
					scripts = env.Skill.Scripts
				}
			}
			return message, skillMD, scripts, true
		}
	}

	// Legacy fallback: recover artifacts from fenced output and show the
	// raw response as the message.
	skillMD, scripts = extractArtifacts(response)
	return strings.TrimSpace(response), skillMD, scripts, false
}

// looksLikeEnvelope guards against a false-positive structured parse when
// the model emits some *other* JSON object (e.g. a bare tool-output sample)
// that happens to unmarshal into skillEnvelope with all-zero fields. We
// require the literal "message" or "skill" key to be present.
func looksLikeEnvelope(jsonStr string) bool {
	return strings.Contains(jsonStr, `"message"`) || strings.Contains(jsonStr, `"skill"`)
}

// extractJSONObject returns the outermost JSON object in s, tolerating the
// common ways an LLM wraps it: a leading ```json fence, surrounding prose,
// or a clean bare object. Returns "" when no braces are found.
//
// It scans for the first '{' and the matching '}' by brace depth while
// skipping over string literals (so a '}' inside a JSON string value —
// very likely in an embedded SKILL.md — doesn't terminate the object
// early). This is what makes the {skill_md: "...markdown with { }..."}
// payload parse reliably.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
