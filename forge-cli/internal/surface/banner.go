// Package surface implements the interactive `forge` surface: the bare-`forge`
// experience that greets the user with the forge logo and helps them build
// Forge agents — either by shelling into a coding agent (Claude Code) wired up
// with forge knowledge + tools, or via forge's own in-process agent loop.
package surface

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// logoLines is the FORGE block wordmark shared by `forge try` and the surface.
var logoLines = []string{
	`███████╗ ██████╗ ██████╗  ██████╗ ███████╗`,
	`██╔════╝██╔═══██╗██╔══██╗██╔════╝ ██╔════╝`,
	`█████╗  ██║   ██║██████╔╝██║  ███╗█████╗  `,
	`██╔══╝  ██║   ██║██╔══██╗██║   ██║██╔══╝  `,
	`██║     ╚██████╔╝██║  ██║╚██████╔╝███████╗`,
	`╚═╝      ╚═════╝ ╚═╝  ╚═╝ ╚═════╝ ╚══════╝`,
}

// logoShades is the forge-heat gradient: bright at the top, warm at the base.
var logoShades = []string{"#fdba74", "#fb923c", "#f97316", "#f97316", "#ea580c", "#c2410c"}

// Logo renders the FORGE block wordmark with a vertical orange gradient
// (plain, uncolored, when color is off — a non-TTY / NO_COLOR).
func Logo(color bool) string {
	var b strings.Builder
	b.WriteByte('\n')
	for i, ln := range logoLines {
		row := "  " + ln
		if color {
			row = lipgloss.NewStyle().Foreground(lipgloss.Color(logoShades[i])).Render(row)
		}
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return b.String()
}

// capability is one "what you can do with forge" highlight shown under the logo.
type capability struct{ verb, rest string }

var capabilities = []capability{
	{"Build", "scaffold an agent from a description — SKILL.md, forge.yaml, tools"},
	{"Wire", "channels (Slack, Telegram, …), MCP servers, and skills"},
	{"Run", "validate, run locally, then build & deploy a hardened container"},
	{"Ask", "how-to questions about forge — config, skills, security, deploy"},
}

// PrintIntro prints the branded header: the FORGE logo, a one-line tagline, and
// the capability highlights. Degrades to plain text when color is unavailable.
func PrintIntro(out io.Writer, color bool) {
	accent := accentFn(color)
	dim := dimFn(color)

	if color {
		_, _ = fmt.Fprint(out, Logo(true))
	} else {
		_, _ = fmt.Fprintf(out, "\n  F O R G E\n")
	}
	_, _ = fmt.Fprintf(out, "  %s\n\n", dim("build Forge agents by talking to forge. Ctrl-D or /exit to quit."))

	for _, c := range capabilities {
		_, _ = fmt.Fprintf(out, "  %s  %s\n", accent(fmt.Sprintf("%-6s", c.verb)), dim(c.rest))
	}
	_, _ = fmt.Fprintln(out)
}

// Dim returns a grey styler (identity when color is off), for callers outside
// this package.
func Dim(color bool) func(string) string { return dimFn(color) }

// Accent returns a forge-orange styler (identity when color is off), for callers
// outside this package.
func Accent(color bool) func(string) string { return accentFn(color) }

// accentFn returns a forge-orange styler (identity when color is off).
func accentFn(color bool) func(string) string {
	if !color {
		return func(s string) string { return s }
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color("#f97316")).Bold(true)
	return func(s string) string { return st.Render(s) }
}

// dimFn returns a grey styler (identity when color is off).
func dimFn(color bool) func(string) string {
	if !color {
		return func(s string) string { return s }
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	return func(s string) string { return st.Render(s) }
}
