package parser

import (
	"strings"
	"testing"
)

const skillWithUses = `---
name: linear-triage
description: Triage Linear issues
metadata:
  forge:
    requires:
      bins: [jq]
    uses:
      - type: mcp
        ref: mcp.linear
        operations: [create_issue, list_issues]
      - type: binary
        ref: bin.jq
        operations: [exec]
---
# Linear triage
`

func TestExtractForgeUses(t *testing.T) {
	_, meta, err := ParseWithMetadata(strings.NewReader(skillWithUses))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	uses := ExtractForgeUses(meta)
	if len(uses) != 2 {
		t.Fatalf("want 2 deps, got %d: %+v", len(uses), uses)
	}
	if uses[0].Type != "mcp" || uses[0].Ref != "mcp.linear" || len(uses[0].Operations) != 2 {
		t.Fatalf("mcp dep mismatch: %+v", uses[0])
	}
	if uses[1].Type != "binary" || uses[1].Ref != "bin.jq" {
		t.Fatalf("binary dep mismatch: %+v", uses[1])
	}
	// requires.bins still parses beside it — the legacy surface is untouched.
	reqs, _, _ := ExtractForgeReqs(meta)
	if reqs == nil || len(reqs.Bins) != 1 || reqs.Bins[0].Name != "jq" {
		t.Fatalf("requires.bins lost: %+v", reqs)
	}
}

// A skill with no uses block — the universal pre-registry case — must parse
// exactly as before, with ExtractForgeUses returning nil.
func TestExtractForgeUses_AbsentIsNil(t *testing.T) {
	const legacy = "---\nname: x\nmetadata:\n  forge:\n    requires:\n      bins: [curl]\n---\nbody\n"
	_, meta, err := ParseWithMetadata(strings.NewReader(legacy))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if uses := ExtractForgeUses(meta); uses != nil {
		t.Fatalf("want nil, got %+v", uses)
	}
}
