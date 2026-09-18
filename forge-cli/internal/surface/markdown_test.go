package surface

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	md := "Here's the **workflow**:\n\n```sh\nforge init my-agent --from-skill-dir ./skill\n```\n\n1. one\n2. two `inline`"

	// color=false (pipes / NO_COLOR / CI) returns the source unchanged.
	if RenderMarkdown(md, false) != md {
		t.Error("color=false should return the markdown unchanged")
	}

	// color=true must not panic or drop content. Under a TTY glamour styles it;
	// under `go test` (no TTY) it degrades to plain — either way it returns
	// non-empty text that preserves the command.
	out := RenderMarkdown(md, true)
	if strings.TrimSpace(out) == "" {
		t.Fatal("color=true returned empty output")
	}
	if !strings.Contains(out, "forge init my-agent") {
		t.Errorf("rendered output dropped the command: %q", out)
	}
}
