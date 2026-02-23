package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// RenderBanner returns the branded header for the wizard.
func RenderBanner(styles *StyleSet, version string, width int) string {
	if version == "" {
		version = "dev"
	}

	forge := styles.Banner.Render("⚒  F O R G E") + "  " + styles.VersionPill.Render("v"+version)
	subtitle := styles.Subtitle.Render("Turn a SKILL.md into a portable, secure, runnable AI agent.")

	dividerWidth := width - 4
	if dividerWidth < 20 {
		dividerWidth = 20
	}
	if dividerWidth > 60 {
		dividerWidth = 60
	}
	divider := lipgloss.NewStyle().
		Foreground(styles.Theme.Border).
		Render(strings.Repeat("─", dividerWidth))

	return fmt.Sprintf("  %s\n  %s\n  %s\n\n", forge, subtitle, divider)
}
