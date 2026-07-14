package forgeui

import (
	"os"
	"strings"
	"testing"
)

// skillBuilderMDPath is the knowledge-skill port of the Skill Builder prompt,
// relative to this package. The sync-docs rule requires its body to stay
// byte-identical to skillBuilderPromptBase (apart from the frontmatter + intro
// wrapper the md adds).
const skillBuilderMDPath = "../.claude/skills/forge-skill-builder.md"

// TestForgeSkillBuilderMD_MirrorsPrompt pins the doc-sync invariant: the body
// of .claude/skills/forge-skill-builder.md (everything after its intro) must
// equal skillBuilderPromptBase verbatim. Before this, the md had silently
// drifted several prompt rewrites behind the Go constant (#252/#270/#297 never
// re-ported), so a new default builtin like web_fetch (#266) was advertised in
// the UI prompt but not in the packaged skill. Fails loudly on the next drift.
func TestForgeSkillBuilderMD_MirrorsPrompt(t *testing.T) {
	raw, err := os.ReadFile(skillBuilderMDPath)
	if err != nil {
		t.Fatalf("read %s: %v", skillBuilderMDPath, err)
	}
	md := string(raw)

	// The prompt body begins at its first line; the lines above it are the
	// md-only frontmatter + "How to use this skill" intro.
	const promptStart = "You are the Forge Skill Designer,"
	idx := strings.Index(md, promptStart)
	if idx < 0 {
		t.Fatalf("%s does not contain the prompt body (looked for %q)", skillBuilderMDPath, promptStart)
	}
	gotBody := strings.TrimSpace(md[idx:])
	wantBody := strings.TrimSpace(skillBuilderPromptBase)

	if gotBody != wantBody {
		// Point at the first divergence so the fix is obvious.
		min := len(gotBody)
		if len(wantBody) < min {
			min = len(wantBody)
		}
		div := min
		for i := 0; i < min; i++ {
			if gotBody[i] != wantBody[i] {
				div = i
				break
			}
		}
		lo := div - 60
		if lo < 0 {
			lo = 0
		}
		t.Fatalf("%s body has drifted from skillBuilderPromptBase near offset %d.\n"+
			"Re-port the constant into the md (keep the frontmatter + intro).\n"+
			"  md:   ...%q...\n  want: ...%q...",
			skillBuilderMDPath, div, snip(gotBody, lo, div+40), snip(wantBody, lo, div+40))
	}
}

func snip(s string, lo, hi int) string {
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}
