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
