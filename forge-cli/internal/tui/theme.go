package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// TermTheme holds all color values for a TUI theme.
type TermTheme struct {
	Name string

	// Brand
	Accent    lipgloss.Color
	AccentDim lipgloss.Color

	// Semantic
	Success lipgloss.Color
	Warning lipgloss.Color
	Error   lipgloss.Color

	// Text
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Dim       lipgloss.Color

	// Surfaces
	Surface      lipgloss.Color
	Border       lipgloss.Color
	ActiveBorder lipgloss.Color
	ActiveBg     lipgloss.Color
}

// DarkTheme is the default dark terminal theme.
var DarkTheme = TermTheme{
	Name:         "dark",
	Accent:       lipgloss.Color("#f97316"),
	AccentDim:    lipgloss.Color("#c2410c"),
	Success:      lipgloss.Color("#22c55e"),
	Warning:      lipgloss.Color("#eab308"),
	Error:        lipgloss.Color("#ef4444"),
	Primary:      lipgloss.Color("#e0e0e8"),
	Secondary:    lipgloss.Color("#888888"),
	Dim:          lipgloss.Color("#5a5a70"),
	Surface:      lipgloss.Color("#1a1a24"),
	Border:       lipgloss.Color("#2a2a3a"),
	ActiveBorder: lipgloss.Color("#f97316"),
	ActiveBg:     lipgloss.Color("#1c1408"),
}

// LightTheme is the light terminal theme.
var LightTheme = TermTheme{
	Name:         "light",
	Accent:       lipgloss.Color("#c2410c"),
	AccentDim:    lipgloss.Color("#7c2d12"),
	Success:      lipgloss.Color("#15803d"),
	Warning:      lipgloss.Color("#a16207"),
	Error:        lipgloss.Color("#b91c1c"),
	Primary:      lipgloss.Color("#0f172a"),
	Secondary:    lipgloss.Color("#374151"),
	Dim:          lipgloss.Color("#4b5563"),
	Surface:      lipgloss.Color("#ffffff"),
	Border:       lipgloss.Color("#d1d5db"),
	ActiveBorder: lipgloss.Color("#c2410c"),
	ActiveBg:     lipgloss.Color("#fff7ed"),
}

// DetectTheme returns the appropriate theme based on flag, env, or detection.
func DetectTheme(flagVal string) TermTheme {
	// 1. --theme flag
	switch strings.ToLower(flagVal) {
	case "dark":
		return DarkTheme
	case "light":
		return LightTheme
	}

	// 2. FORGE_THEME env
	if env := os.Getenv("FORGE_THEME"); env != "" {
		switch strings.ToLower(env) {
		case "dark":
			return DarkTheme
		case "light":
			return LightTheme
		}
	}

	// 3. COLORFGBG heuristic (format: "fg;bg")
	if colorfgbg := os.Getenv("COLORFGBG"); colorfgbg != "" {
		parts := strings.Split(colorfgbg, ";")
		if len(parts) >= 2 {
			bg := parts[len(parts)-1]
			// bg values 0-6 or "0" are typically dark backgrounds
			// bg values 7-15 are typically light backgrounds
			if bg == "15" || bg == "7" {
				return LightTheme
			}
		}
	}

	// 4. Default to dark
	return DarkTheme
}

// StyleSet contains pre-computed lipgloss styles derived from a theme.
type StyleSet struct {
	Theme TermTheme

	// Text styles
	Title        lipgloss.Style
	Subtitle     lipgloss.Style
	AccentTxt    lipgloss.Style
	DimTxt       lipgloss.Style
	SuccessTxt   lipgloss.Style
	WarningTxt   lipgloss.Style
	ErrorTxt     lipgloss.Style
	PrimaryTxt   lipgloss.Style
	SecondaryTxt lipgloss.Style

	// Border styles
	ActiveBorder   lipgloss.Style
	InactiveBorder lipgloss.Style

	// Item styles
	SelectedItem   lipgloss.Style
	UnselectedItem lipgloss.Style
	Cursor         lipgloss.Style

	// Kbd hint
	KbdKey  lipgloss.Style
	KbdDesc lipgloss.Style

	// Banner
	Banner lipgloss.Style

	// Summary
	SummaryKey   lipgloss.Style
	SummaryValue lipgloss.Style

	// Bordered box
	BorderedBox lipgloss.Style

	// Badge styles (filled background with contrasting text)
	StepBadgeComplete lipgloss.Style
	StepBadgeActive   lipgloss.Style
	StepBadgePending  lipgloss.Style

	// Version pill
	VersionPill lipgloss.Style
}

// NewStyleSet creates a StyleSet from a theme.
func NewStyleSet(theme TermTheme) *StyleSet {
	return &StyleSet{
		Theme: theme,

		Title:        lipgloss.NewStyle().Foreground(theme.Accent).Bold(true),
		Subtitle:     lipgloss.NewStyle().Foreground(theme.Secondary),
		AccentTxt:    lipgloss.NewStyle().Foreground(theme.Accent),
		DimTxt:       lipgloss.NewStyle().Foreground(theme.Dim),
		SuccessTxt:   lipgloss.NewStyle().Foreground(theme.Success),
		WarningTxt:   lipgloss.NewStyle().Foreground(theme.Warning),
		ErrorTxt:     lipgloss.NewStyle().Foreground(theme.Error),
		PrimaryTxt:   lipgloss.NewStyle().Foreground(theme.Primary),
		SecondaryTxt: lipgloss.NewStyle().Foreground(theme.Secondary),

		ActiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.ActiveBorder),
		InactiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Border),

		SelectedItem: lipgloss.NewStyle().
			Foreground(theme.Primary).
			Bold(true),
		UnselectedItem: lipgloss.NewStyle().
			Foreground(theme.Secondary),
		Cursor: lipgloss.NewStyle().
			Foreground(theme.Accent),

		KbdKey: lipgloss.NewStyle().
			Foreground(theme.Primary).
			Background(theme.Dim).
			Padding(0, 1),
		KbdDesc: lipgloss.NewStyle().
			Foreground(theme.Dim),

		Banner: lipgloss.NewStyle().
			Foreground(theme.Accent).
			Bold(true),

		SummaryKey: lipgloss.NewStyle().
			Foreground(theme.Secondary).
			Width(16),
		SummaryValue: lipgloss.NewStyle().
			Foreground(theme.Primary).
			Bold(true),

		BorderedBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Border).
			Padding(0, 1),

		StepBadgeComplete: lipgloss.NewStyle().
			Background(theme.Success).
			Foreground(lipgloss.Color("#ffffff")).
			Bold(true).
			Padding(0, 1),
		StepBadgeActive: lipgloss.NewStyle().
			Background(theme.Accent).
			Foreground(lipgloss.Color("#ffffff")).
			Bold(true).
			Padding(0, 1),
		StepBadgePending: lipgloss.NewStyle().
			Background(theme.Border).
			Foreground(theme.Secondary).
			Padding(0, 1),
		VersionPill: lipgloss.NewStyle().
			Background(theme.Accent).
			Foreground(lipgloss.Color("#ffffff")).
			Padding(0, 1).
			Bold(true),
	}
}
