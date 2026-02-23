package steps

import (
	"fmt"
	"os"
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
	OneOfEnv      []string
	OptionalEnv   []string
	RequiredBins  []string
	EgressDomains []string
}

type skillsPhase int

const (
	skillsSelectPhase skillsPhase = iota
	skillsEnvPhase
)

// envPrompt describes a single env var prompt to show.
type envPrompt struct {
	envVar    string
	label     string
	allowSkip bool
	skillName string
	kind      string // "required", "one_of", "optional"
}

// SkillsStep handles external skill selection.
type SkillsStep struct {
	styles      *tui.StyleSet
	allSkills   []SkillInfo
	multiSelect components.MultiSelect
	phase       skillsPhase
	complete    bool
	selected    []string
	empty       bool

	// Env prompting
	envPrompts    []envPrompt
	currentPrompt int
	keyInput      components.SecretInput
	envValues     map[string]string
	knownEnvVars  map[string]string // env vars already collected by earlier steps
}

// NewSkillsStep creates a new skills selection step.
func NewSkillsStep(styles *tui.StyleSet, skills []SkillInfo) *SkillsStep {
	if len(skills) == 0 {
		return &SkillsStep{
			styles:    styles,
			complete:  false,
			empty:     true,
			envValues: make(map[string]string),
		}
	}

	var items []components.MultiSelectItem
	for _, sk := range skills {
		icon := skillIcon(sk.Name)
		var reqs []string
		if len(sk.RequiredBins) > 0 {
			reqs = append(reqs, "bins: "+strings.Join(sk.RequiredBins, ", "))
		}
		if len(sk.RequiredEnv) > 0 {
			reqs = append(reqs, "env: "+strings.Join(sk.RequiredEnv, ", "))
		}
		if len(sk.OneOfEnv) > 0 {
			reqs = append(reqs, "one of: "+strings.Join(sk.OneOfEnv, " / "))
		}
		var reqLine string
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
		allSkills:   skills,
		multiSelect: ms,
		envValues:   make(map[string]string),
	}
}

// Prepare captures env vars already collected by earlier wizard steps.
func (s *SkillsStep) Prepare(ctx *tui.WizardContext) {
	s.knownEnvVars = make(map[string]string)
	for k, v := range ctx.EnvVars {
		s.knownEnvVars[k] = v
	}
}

func (s *SkillsStep) Title() string { return "External Skills" }
func (s *SkillsStep) Icon() string  { return "📦" }

func (s *SkillsStep) Init() tea.Cmd {
	s.complete = false
	s.phase = skillsSelectPhase
	s.currentPrompt = 0
	s.envPrompts = nil
	s.envValues = make(map[string]string)
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

	switch s.phase {
	case skillsSelectPhase:
		updated, cmd := s.multiSelect.Update(msg)
		s.multiSelect = updated

		if s.multiSelect.Done() {
			s.selected = s.multiSelect.SelectedValues()

			// Build env prompts for selected skills
			s.buildEnvPrompts()

			if len(s.envPrompts) == 0 {
				s.complete = true
				return s, func() tea.Msg { return tui.StepCompleteMsg{} }
			}

			// Start env prompting
			s.phase = skillsEnvPhase
			s.currentPrompt = 0
			s.initCurrentPrompt()
			return s, s.keyInput.Init()
		}

		return s, cmd

	case skillsEnvPhase:
		updated, cmd := s.keyInput.Update(msg)
		s.keyInput = updated

		if s.keyInput.Done() {
			val := s.keyInput.Value()
			prompt := s.envPrompts[s.currentPrompt]
			if val != "" {
				s.envValues[prompt.envVar] = val
			}

			s.currentPrompt++

			// Check if we're done with all prompts
			if s.currentPrompt >= len(s.envPrompts) {
				// Check one_of groups
				if s.checkOneOfGroups() {
					s.complete = true
					return s, func() tea.Msg { return tui.StepCompleteMsg{} }
				}
				// One or more one_of groups unsatisfied — prompts were appended
			}

			s.initCurrentPrompt()
			return s, s.keyInput.Init()
		}

		return s, cmd
	}

	return s, nil
}

// envAlreadyKnown returns true if the env var is already set in OS env or
// was collected by an earlier wizard step (provider key, web search key, etc.).
func (s *SkillsStep) envAlreadyKnown(env string) bool {
	if os.Getenv(env) != "" {
		return true
	}
	if v, ok := s.knownEnvVars[env]; ok && v != "" {
		return true
	}
	return false
}

// buildEnvPrompts creates the list of env prompts for selected skills.
func (s *SkillsStep) buildEnvPrompts() {
	s.envPrompts = nil
	seen := make(map[string]bool)

	for _, skillName := range s.selected {
		sk := s.findSkill(skillName)
		if sk == nil {
			continue
		}

		// Required env vars
		for _, env := range sk.RequiredEnv {
			if seen[env] || s.envAlreadyKnown(env) {
				continue
			}
			seen[env] = true
			s.envPrompts = append(s.envPrompts, envPrompt{
				envVar:    env,
				label:     fmt.Sprintf("%s (required by %s)", env, sk.DisplayName),
				allowSkip: false,
				skillName: sk.Name,
				kind:      "required",
			})
		}

		// One-of env vars
		if len(sk.OneOfEnv) > 0 {
			// Check if any one-of is already available
			anySet := false
			for _, env := range sk.OneOfEnv {
				if s.envAlreadyKnown(env) {
					anySet = true
					break
				}
			}
			if !anySet {
				for _, env := range sk.OneOfEnv {
					if seen[env] {
						continue
					}
					seen[env] = true
					s.envPrompts = append(s.envPrompts, envPrompt{
						envVar:    env,
						label:     fmt.Sprintf("%s (one of %s — %s)", env, strings.Join(sk.OneOfEnv, " / "), sk.DisplayName),
						allowSkip: true, // initially skippable, but group must have at least one
						skillName: sk.Name,
						kind:      "one_of",
					})
				}
			}
		}

		// Optional env vars
		for _, env := range sk.OptionalEnv {
			if seen[env] || s.envAlreadyKnown(env) {
				continue
			}
			seen[env] = true
			s.envPrompts = append(s.envPrompts, envPrompt{
				envVar:    env,
				label:     fmt.Sprintf("%s (optional — %s)", env, sk.DisplayName),
				allowSkip: true,
				skillName: sk.Name,
				kind:      "optional",
			})
		}
	}
}

// checkOneOfGroups verifies that all one_of groups have at least one value.
// If not, appends a mandatory re-prompt and returns false.
func (s *SkillsStep) checkOneOfGroups() bool {
	// Collect one_of skills that need checking
	type group struct {
		skillName string
		envVars   []string
	}
	seen := make(map[string]bool)
	var groups []group

	for _, p := range s.envPrompts {
		if p.kind != "one_of" || seen[p.skillName] {
			continue
		}
		seen[p.skillName] = true
		sk := s.findSkill(p.skillName)
		if sk != nil {
			groups = append(groups, group{skillName: p.skillName, envVars: sk.OneOfEnv})
		}
	}

	allSatisfied := true
	for _, g := range groups {
		hasValue := false
		for _, env := range g.envVars {
			if v, ok := s.envValues[env]; ok && v != "" {
				hasValue = true
				break
			}
		}
		if !hasValue {
			// Re-prompt the first env var as required
			sk := s.findSkill(g.skillName)
			displayName := g.skillName
			if sk != nil {
				displayName = sk.DisplayName
			}
			label := fmt.Sprintf("%s (required — at least one needed for %s)", g.envVars[0], displayName)
			s.envPrompts = append(s.envPrompts, envPrompt{
				envVar:    g.envVars[0],
				label:     label,
				allowSkip: false,
				skillName: g.skillName,
				kind:      "required",
			})
			allSatisfied = false
		}
	}

	return allSatisfied
}

func (s *SkillsStep) initCurrentPrompt() {
	if s.currentPrompt >= len(s.envPrompts) {
		return
	}
	prompt := s.envPrompts[s.currentPrompt]
	s.keyInput = components.NewSecretInput(
		prompt.label,
		prompt.allowSkip,
		s.styles.Theme.Accent,
		s.styles.Theme.Success,
		s.styles.Theme.Error,
		s.styles.Theme.Border,
		s.styles.AccentTxt,
		s.styles.InactiveBorder,
		s.styles.SuccessTxt,
		s.styles.ErrorTxt,
		s.styles.DimTxt,
		s.styles.KbdKey,
		s.styles.KbdDesc,
	)
}

func (s *SkillsStep) findSkill(name string) *SkillInfo {
	for i := range s.allSkills {
		if s.allSkills[i].Name == name {
			return &s.allSkills[i]
		}
	}
	return nil
}

func (s *SkillsStep) View(width int) string {
	if s.empty {
		return fmt.Sprintf("  %s\n", s.styles.DimTxt.Render("No skills available in registry."))
	}
	switch s.phase {
	case skillsSelectPhase:
		return s.multiSelect.View(width)
	case skillsEnvPhase:
		return s.keyInput.View(width)
	}
	return ""
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
	for k, v := range s.envValues {
		ctx.EnvVars[k] = v
	}
}

func skillIcon(name string) string {
	icons := map[string]string{
		"summarize":     "🧾",
		"github":        "🐙",
		"weather":       "🌤️",
		"tavily-search": "🔍",
	}
	if icon, ok := icons[name]; ok {
		return icon
	}
	return "📦"
}
