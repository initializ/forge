package runtime

import (
	"strings"
	"testing"
)

// codeAgentDirective is a prompt string, so no runtime path exercises it and a
// regression would ship silently. These assertions pin the invariant its doc
// comment describes: acting-over-narrating pressure stays scoped to coding
// work, and conversation keeps a legal text-only answer.
//
// The directive once opened with an unconditional "Every response MUST include
// tool calls. NEVER respond with only text." The system prompt is built once at
// startup and never sees the incoming message, so that also governed a plain
// "Hi" — leaving the model no legal move, so it discharged the obligation
// through the nearest write tool and saved its greeting to a file.
func TestCodeAgentDirectiveScopesConversationCarveOut(t *testing.T) {
	// The carve-out that gives a greeting somewhere legal to go.
	for _, want := range []string{
		"CONVERSATION",
		"normal text reply in chat",
		"write your reply to a file",
	} {
		if !strings.Contains(strings.ToLower(codeAgentDirective), strings.ToLower(want)) {
			t.Errorf("conversation carve-out lost: codeAgentDirective no longer mentions %q", want)
		}
	}

	// The prohibition blocks must stay qualified, or they silently re-apply to
	// conversation.
	for _, section := range []string{
		"FORBIDDEN (on coding requests):",
		"REQUIRED (on coding requests):",
	} {
		if !strings.Contains(codeAgentDirective, section) {
			t.Errorf("section lost its coding-request qualifier: want %q", section)
		}
	}
}

// TestCodeAgentDirectiveHasNoUnconditionalToolMandate guards the specific
// regression. It checks each mandate is SCOPED rather than banning the wording
// outright, so a correctly qualified rewording ("On a coding request, every
// response MUST include tool calls") still passes.
func TestCodeAgentDirectiveHasNoUnconditionalToolMandate(t *testing.T) {
	// Phrases that are only safe when the surrounding line limits them to
	// coding work. Unqualified, each one removes the model's legal text-only
	// response and turns a greeting into a file write.
	mandates := []string{
		"every response must include tool calls",
		"never respond with only text",
	}

	for i, line := range strings.Split(codeAgentDirective, "\n") {
		lower := strings.ToLower(line)
		for _, m := range mandates {
			if !strings.Contains(lower, m) {
				continue
			}
			// Match "coding request", not bare "coding": the pre-fix line was
			// "You are a coding agent. Every response MUST include tool
			// calls." — a "coding" needle would accept that very regression.
			// "coding request" covers "on coding requests" / "on a coding
			// request" / "CODING request", the qualifier forms used here.
			if !strings.Contains(lower, "coding request") {
				t.Errorf("line %d carries an unconditional tool-call mandate %q:\n\t%s\n"+
					"The system prompt is built once at startup and never sees the incoming "+
					"message, so this governs greetings too. Scope it to coding requests.",
					i+1, m, line)
			}
		}
	}
}
