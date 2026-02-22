package steps

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// SkillInfo represents a registry skill for the skills step.
type SkillInfo struct {
	Name          string
	DisplayName   string
	Description   string
	RequiredEnv   []string
	RequiredBins  []string
	EgressDomains []string
}

// SkillsStep handles external skill selection.
type SkillsStep struct {
	styles      *tui.StyleSet
	multiSelect components.MultiSelect
	complete    bool
	selected    []string
	empty       bool // true if no skills available
}

// NewSkillsStep creates a new skills selection step.
func NewSkillsStep(styles *tui.StyleSet, skills []SkillInfo) *SkillsStep {
	if len(skills) == 0 {
		return &SkillsStep{
			styles:   styles,
			complete: false,
			empty:    true,
		}
	}

	var items []components.MultiSelectItem
	for _, sk := range skills {
		icon := skillIcon(sk.Name)
		var reqLine string
		var reqs []string
		if len(sk.RequiredBins) > 0 {
			reqs = append(reqs, "bins: "+strings.Join(sk.RequiredBins, ", "))
		}
		if len(sk.RequiredEnv) > 0 {
			reqs = append(reqs, "env: "+strings.Join(sk.RequiredEnv, ", "))
		}
		if len(reqs) > 0 {
			reqLine = strings.Join(reqs, " · ")
		}

		items = append(items, components.MultiSelectItem{
			Label:           sk.DisplayName,
			Value:           sk.Name,
			Description:     sk.Description,
			Icon:            icon,
			RequirementLine: reqLine,
		})
	}

	ms := components.NewMultiSelect(
		items,
		styles.Theme.Accent,
		styles.Theme.AccentDim,
		styles.Theme.Primary,
		styles.Theme.Secondary,
		styles.Theme.Dim,
		styles.ActiveBorder,
		styles.InactiveBorder,
		styles.KbdKey,
		styles.KbdDesc,
	)

	return &SkillsStep{
		styles:      styles,
		multiSelect: ms,
	}
}

func (s *SkillsStep) Title() string { return "External Skills" }
func (s *SkillsStep) Icon() string  { return "📦" }

func (s *SkillsStep) Init() tea.Cmd {
	s.complete = false
	if s.empty {
		s.complete = true
		return func() tea.Msg { return tui.StepCompleteMsg{} }
	}
	return s.multiSelect.Init()
}

func (s *SkillsStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	updated, cmd := s.multiSelect.Update(msg)
	s.multiSelect = updated

	if s.multiSelect.Done() {
		s.selected = s.multiSelect.SelectedValues()
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, cmd
}

func (s *SkillsStep) View(width int) string {
	if s.empty {
		return fmt.Sprintf("  %s\n", s.styles.DimTxt.Render("No skills available in registry."))
	}
	return s.multiSelect.View(width)
}

func (s *SkillsStep) Complete() bool {
	return s.complete
}

func (s *SkillsStep) Summary() string {
	if len(s.selected) == 0 {
		return "none"
	}
	return strings.Join(s.selected, ", ")
}

func (s *SkillsStep) Apply(ctx *tui.WizardContext) {
	ctx.Skills = s.selected
}

func skillIcon(name string) string {
	icons := map[string]string{
		"summarize": "🧾",
		"github":    "🐙",
		"weather":   "🌤️",
	}
	if icon, ok := icons[name]; ok {
		return icon
	}
	return "📦"
}
