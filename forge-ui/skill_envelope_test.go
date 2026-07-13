package forgeui

import (
	"strings"
	"testing"
)

// TestParseSkillEnvelope_Interviewing — while still gathering requirements
// the model returns a message with skill:null; no draft is surfaced.
func TestParseSkillEnvelope_Interviewing(t *testing.T) {
	resp := `{"message": "What credential should the skill use?", "skill": null}`
	msg, skillMD, scripts, structured := parseSkillEnvelope(resp)
	if !structured {
		t.Fatal("expected structured parse")
	}
	if msg != "What credential should the skill use?" {
		t.Errorf("message = %q", msg)
	}
	if skillMD != "" {
		t.Errorf("skillMD should be empty while interviewing, got %q", skillMD)
	}
	if len(scripts) != 0 {
		t.Errorf("scripts should be empty, got %v", scripts)
	}
}

// TestParseSkillEnvelope_Draft — once draftable, skill carries the full
// skill_md and any scripts. The skill_md contains braces/newlines (an
// embedded JSON output schema) that must not confuse the brace scanner.
func TestParseSkillEnvelope_Draft(t *testing.T) {
	resp := `{
  "message": "Here is your skill.",
  "skill": {
    "skill_md": "---\nname: demo\n---\n# Demo\n## Tool: demo_tool\n**Output:**\n{\"ok\": true}\n",
    "scripts": {"demo-tool.sh": "#!/bin/bash\nset -euo pipefail\necho '{}'\n"}
  }
}`
	msg, skillMD, scripts, structured := parseSkillEnvelope(resp)
	if !structured {
		t.Fatal("expected structured parse")
	}
	if msg != "Here is your skill." {
		t.Errorf("message = %q", msg)
	}
	if wantSub := "## Tool: demo_tool"; !strings.Contains(skillMD, wantSub) {
		t.Errorf("skillMD missing %q; got %q", wantSub, skillMD)
	}
	if !strings.Contains(skillMD, `{"ok": true}`) {
		t.Errorf("skillMD lost the embedded JSON braces: %q", skillMD)
	}
	if scripts["demo-tool.sh"] == "" {
		t.Errorf("script not recovered: %v", scripts)
	}
}

// TestParseSkillEnvelope_FencedJSON — the model wraps its JSON in a ```json
// fence or adds prose; extractJSONObject still isolates the object.
func TestParseSkillEnvelope_FencedJSON(t *testing.T) {
	resp := "Sure! Here you go:\n```json\n{\"message\": \"done\", \"skill\": null}\n```\n"
	msg, _, _, structured := parseSkillEnvelope(resp)
	if !structured {
		t.Fatal("expected structured parse from fenced JSON")
	}
	if msg != "done" {
		t.Errorf("message = %q", msg)
	}
}

// TestParseSkillEnvelope_LegacyFallback — a model that ignored the JSON
// instruction and emitted the old quadruple-backtick fences still yields a
// recovered skill (structured=false) so nothing is silently lost.
func TestParseSkillEnvelope_LegacyFallback(t *testing.T) {
	resp := "Here's the skill:\n````skill.md\n---\nname: legacy\n---\n# Legacy\n````\n"
	_, skillMD, _, structured := parseSkillEnvelope(resp)
	if structured {
		t.Fatal("expected legacy (non-structured) path")
	}
	if !strings.Contains(skillMD, "name: legacy") {
		t.Errorf("legacy fence extraction failed: %q", skillMD)
	}
}

// TestExtractJSONObject_BraceInString — a '}' inside a string value must not
// terminate the object early (the core reason we scan by depth, skipping
// string literals, instead of using LastIndexByte).
func TestExtractJSONObject_BraceInString(t *testing.T) {
	in := `prefix {"a": "has a } brace", "b": 1} suffix`
	got := extractJSONObject(in)
	want := `{"a": "has a } brace", "b": 1}`
	if got != want {
		t.Errorf("extractJSONObject = %q, want %q", got, want)
	}
}

// TestExtractJSONObject_None — no object present.
func TestExtractJSONObject_None(t *testing.T) {
	if got := extractJSONObject("no json here"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// TestParseSkillEnvelope_IncidentalJSONDoesNotHijack (#276 review finding 1):
// a legacy-format response whose prose contains an incidental JSON sample
// (with only a "message" key) must NOT be mistaken for the envelope — the
// fenced draft must still be recovered via the legacy path.
func TestParseSkillEnvelope_IncidentalJSONDoesNotHijack(t *testing.T) {
	resp := "The tool returns {\"message\": \"ok\"} on success. Here's the skill:\n" +
		"````skill.md\n---\nname: real-skill\n---\n# Real\n````\n"
	msg, skillMD, _, structured := parseSkillEnvelope(resp)
	if structured {
		t.Fatalf("incidental {\"message\":\"ok\"} must not be treated as the envelope; msg=%q", msg)
	}
	if !strings.Contains(skillMD, "name: real-skill") {
		t.Errorf("fenced draft was lost; skillMD=%q", skillMD)
	}
}

// TestParseSkillEnvelope_BracePreambleFindsEnvelope (#276 review finding 2):
// a valid envelope preceded by brace-bearing prose (or a small JSON sample)
// must be found by iterating candidates — not abandoned to the legacy path
// (which would show the raw JSON as the message).
func TestParseSkillEnvelope_BracePreambleFindsEnvelope(t *testing.T) {
	resp := "Sure — for parsing {json} data, here you go: " +
		`{"message": "Here is your skill.", "skill": {"skill_md": "---\nname: demo\n---\n# Demo\n", "scripts": {}}}`
	msg, skillMD, _, structured := parseSkillEnvelope(resp)
	if !structured {
		t.Fatal("valid envelope after brace-bearing preamble should parse as structured")
	}
	if msg != "Here is your skill." {
		t.Errorf("message = %q", msg)
	}
	if !strings.Contains(skillMD, "name: demo") {
		t.Errorf("skill_md not extracted: %q", skillMD)
	}
}

// TestParseSkillEnvelope_InterviewingBothKeys — an interviewing turn carries
// both keys with skill:null and must parse structured (both-keys guard must
// not reject the legitimate null-skill case).
func TestParseSkillEnvelope_InterviewingBothKeys(t *testing.T) {
	resp := `{"message": "What credential?", "skill": null}`
	msg, skillMD, _, structured := parseSkillEnvelope(resp)
	if !structured {
		t.Fatal("interviewing envelope (skill:null) must parse structured")
	}
	if msg != "What credential?" || skillMD != "" {
		t.Errorf("msg=%q skillMD=%q", msg, skillMD)
	}
}
