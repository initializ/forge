package surface

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// RenderMarkdown renders a markdown string for the terminal (headings, bold,
// lists, and fenced code blocks styled), so the native agent's replies don't
// show as raw markdown. Falls back to the original text when color is off or the
// renderer can't initialize.
//
// It uses a FIXED "dark" style deliberately: glamour's WithAutoStyle calls
// termenv.HasDarkBackground(), which sends an OSC-11 query and reads the reply
// from stdin — and the surface REPL has a concurrent goroutine reading stdin, so
// that query would race/hang the terminal. A fixed style never touches the
// terminal, so rendering is safe alongside the stdin reader.
func RenderMarkdown(md string, color bool) string {
	// Scrub control/escape bytes from the (LLM/tool-sourced) content first, so a
	// malicious tool result or model output can't smuggle raw ANSI/OSC sequences
	// straight to the TTY. glamour re-adds only its own styling escapes.
	md = scrubControl(md)
	if !color || strings.TrimSpace(md) == "" {
		return md
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}

// scrubControl removes C0/C1 control characters (including ESC, which starts
// ANSI/OSC sequences) from s, keeping only newline and tab. Printable text and
// multibyte UTF-8 are untouched.
func scrubControl(s string) string {
	if !strings.ContainsFunc(s, isStripControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isStripControl(r) {
			return -1
		}
		return r
	}, s)
}

func isStripControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}
