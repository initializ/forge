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

	// Try EVERY balanced {...} candidate in order, not just the first. A model
	// may precede the real envelope with prose that itself contains JSON —
	// tool-output examples are JSON in this domain — so the first object isn't
	// necessarily the envelope. Iterate until one both parses AND carries the
	// envelope keys; only then commit to the structured path. This closes two
	// silent-draft-loss holes (#276 review): an incidental `{"message":"ok"}`
	// in prose hijacking the parse, and a valid envelope after a brace-bearing
	// preamble being abandoned to the legacy path.
	for from := 0; from < len(response); {
		cand, start := jsonObjectAt(response, from)
		if cand == "" {
			break
		}
		// On any rejection, advance to just past this candidate's opening `{`
		// (not past the whole object), so a real envelope NESTED inside a
		// wrapper — e.g. `{"response": {"message":…, "skill":…}}` — is still
		// reachable on the next iteration (#276 review hardening).
		next := start + 1
		if !looksLikeEnvelope(cand) {
			from = next
			continue
		}
		var env skillEnvelope
		if err := json.Unmarshal([]byte(cand), &env); err != nil {
			from = next
			continue
		}
		// Reject a zero-value "envelope": a wrapper like
		// `{"response": {...}}` contains both key substrings and unmarshals
		// cleanly (unknown top-level field ignored) but yields no message and
		// no skill. Skip it so the inner real envelope (or the legacy
		// fallback) wins instead of committing an empty structured result.
		if env.Message == "" && env.Skill == nil {
			from = next
			continue
		}
		message = strings.TrimSpace(env.Message)
		if env.Skill != nil {
			skillMD = strings.TrimSpace(env.Skill.SkillMD)
			if env.Skill.Scripts != nil {
				scripts = env.Skill.Scripts
			}
		}
		return message, skillMD, scripts, true
	}

	// Legacy fallback: recover artifacts from fenced output and show the
	// raw response as the message.
	skillMD, scripts = extractArtifacts(response)
	return strings.TrimSpace(response), skillMD, scripts, false
}

// looksLikeEnvelope guards against a false-positive structured parse on some
// OTHER JSON object in the response. It requires BOTH the "message" and
// "skill" keys — the prompt mandates exactly those two fields, so a real
// envelope always carries both (even while interviewing, `skill` is present
// as null). Requiring only one would let an incidental `{"message":"ok"}` in
// prose hijack the parse and drop a fenced draft (#276 review).
func looksLikeEnvelope(jsonStr string) bool {
	return strings.Contains(jsonStr, `"message"`) && strings.Contains(jsonStr, `"skill"`)
}

// extractJSONObject returns the FIRST balanced {...} object in s, or "".
// Retained for callers/tests that only need the first object;
// parseSkillEnvelope iterates all candidates via jsonObjectAt.
func extractJSONObject(s string) string {
	obj, _ := jsonObjectAt(s, 0)
	return obj
}

// jsonObjectAt finds the next balanced {...} object at or after `from`,
// tolerating a leading ```json fence or surrounding prose. It scans by brace
// depth while skipping string literals (so a '}' inside a JSON string — very
// likely in an embedded SKILL.md — doesn't terminate the object early).
// Returns the object substring and the index of its opening '{', so the
// caller can resume scanning just past it (start+1) to reach nested
// candidates. Returns ("", -1) when no complete object remains at/after
// `from`.
func jsonObjectAt(s string, from int) (obj string, start int) {
	rel := strings.IndexByte(s[from:], '{')
	if rel < 0 {
		return "", -1
	}
	start = from + rel
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
				return s[start : i+1], start
			}
		}
	}
	return "", -1 // unbalanced — no complete object
}
