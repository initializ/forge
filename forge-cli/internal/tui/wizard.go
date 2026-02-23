package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// WizardContext accumulates all data across wizard steps.
type WizardContext struct {
	Name          string
	Provider      string
	APIKey        string
	Channel       string
	ChannelTokens map[string]string
	BuiltinTools  []string
	Skills        []string
	EgressDomains []string
	CustomBaseURL string
	CustomModel   string
	CustomAPIKey  string
	EnvVars       map[string]string
}

// NewWizardContext creates an initialized WizardContext.
func NewWizardContext() *WizardContext {
	return &WizardContext{
		ChannelTokens: make(map[string]string),
		EnvVars:       make(map[string]string),
	}
}

// WizardModel is the top-level bubbletea model that orchestrates the wizard.
type WizardModel struct {
	styles  *StyleSet
	theme   TermTheme
	steps   []Step
	current int
	ctx     *WizardContext
	width   int
	height  int
	done    bool
	err     error
	version string
}

// NewWizardModel creates a new wizard with the given steps.
func NewWizardModel(theme TermTheme, steps []Step, version string) WizardModel {
	return WizardModel{
		styles:  NewStyleSet(theme),
		theme:   theme,
		steps:   steps,
		ctx:     NewWizardContext(),
		width:   80,
		height:  24,
		version: version,
	}
}

// Init initializes the first step.
func (w WizardModel) Init() tea.Cmd {
	if len(w.steps) > 0 {
		return w.steps[0].Init()
	}
	return nil
}

// advanceStep applies the current step's data and moves to the next one.
func (w *WizardModel) advanceStep() tea.Cmd {
	if w.current < len(w.steps) {
		w.steps[w.current].Apply(w.ctx)
	}

	w.current++
	if w.current >= len(w.steps) {
		w.done = true
		return tea.Quit
	}

	// Prepare the next step if it supports it
	if preparer, ok := w.steps[w.current].(interface{ Prepare(ctx *WizardContext) }); ok {
		preparer.Prepare(w.ctx)
	}
	return w.steps[w.current].Init()
}

// Update handles messages for the wizard.
func (w WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		return w, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			w.err = fmt.Errorf("wizard cancelled")
			return w, tea.Quit
		}

	case StepBackMsg:
		if w.current > 0 {
			w.current--
			return w, w.steps[w.current].Init()
		}
		return w, nil

	case StepCompleteMsg:
		// This is the sole path for step advancement.
		cmd := w.advanceStep()
		return w, cmd
	}

	// Delegate to current step
	if w.current < len(w.steps) {
		updated, cmd := w.steps[w.current].Update(msg)
		w.steps[w.current] = updated
		// Steps signal completion only via StepCompleteMsg — never check Complete() here.
		return w, cmd
	}

	return w, nil
}

// View renders the entire wizard UI.
func (w WizardModel) View() string {
	var out string

	// Banner
	out += "\n" + RenderBanner(w.styles, w.version, w.width)
	out += "\n"

	// Step progress (completed steps)
	out += RenderProgress(w.steps, w.current, w.styles, w.width)
	out += "\n"

	// Active step content
	if w.current < len(w.steps) {
		out += w.steps[w.current].View(w.width)
	}
	out += "\n"

	return out
}

// Context returns the accumulated wizard context.
func (w WizardModel) Context() *WizardContext {
	return w.ctx
}

// Err returns any error that occurred during the wizard.
func (w WizardModel) Err() error {
	return w.err
}

// Done returns true if the wizard completed successfully.
func (w WizardModel) Done() bool {
	return w.done
}
