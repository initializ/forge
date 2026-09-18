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

func TestScrubControl(t *testing.T) {
	// A malicious tool result trying to smuggle a raw ANSI/OSC escape.
	evil := "hello \x1b[31mRED\x1b[0m and \x1b]0;title\x07 done"
	got := scrubControl(evil)
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x07') {
		t.Errorf("control chars survived scrub: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "RED") || !strings.Contains(got, "done") {
		t.Errorf("scrub dropped printable text: %q", got)
	}
	// Newlines and tabs are preserved; multibyte UTF-8 untouched.
	if scrubControl("a\n\tb — é") != "a\n\tb — é" {
		t.Errorf("scrub altered safe text: %q", scrubControl("a\n\tb — é"))
	}
	// RenderMarkdown(color=false) also scrubs.
	if strings.ContainsRune(RenderMarkdown(evil, false), '\x1b') {
		t.Error("RenderMarkdown should scrub control chars even when color is off")
	}
}
