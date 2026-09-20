package surface

import (
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// AgentChoice is the coding-agent selection from the chooser.
type AgentChoice string

const (
	// ChoiceClaude shells into Claude Code wired with forge knowledge + tools.
	ChoiceClaude AgentChoice = "claude"
	// ChoiceNative runs forge's own in-process builder agent.
	ChoiceNative AgentChoice = "native"
)

// chooser palette (forge-heat), independent of the wizard theme.
var (
	colAccent    = lipgloss.Color("#f97316")
	colPrimary   = lipgloss.Color("#f5f5f5")
	colSecondary = lipgloss.Color("#bbbbbb")
	colDim       = lipgloss.Color("#777777")
	colBorder    = lipgloss.Color("#444444")
	colActive    = lipgloss.Color("#f97316")
)

// selectModel is a minimal bubbletea wrapper that runs one SingleSelect inline
// (no alt-screen, so the banner above it stays visible) and captures the pick.
type selectModel struct {
	title    string
	sel      components.SingleSelect
	width    int
	quit     bool
	canceled bool
}

func (m selectModel) Init() tea.Cmd { return m.sel.Init() }

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		if s := msg.String(); s == "ctrl+c" || s == "esc" || s == "q" {
			m.canceled = true
			m.quit = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.sel, cmd = m.sel.Update(msg)
	if m.sel.Done() {
		m.quit = true
		return m, tea.Quit
	}
	return m, cmd
}

func (m selectModel) View() string {
	w := m.width
	if w <= 0 {
		w = 72
	}
	title := lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render(m.title)
	return "\n  " + title + "\n\n" + m.sel.View(w)
}

// newSelect builds a SingleSelect over the given items with the forge palette.
func newSelect(items []components.SingleSelectItem, preselect string) components.SingleSelect {
	kbdKey := lipgloss.NewStyle().Foreground(colAccent)
	kbdDesc := lipgloss.NewStyle().Foreground(colDim)
	sel := components.NewSingleSelect(items, colAccent, colPrimary, colSecondary, colDim,
		colBorder, colActive, lipgloss.Color(""), kbdKey, kbdDesc)
	if preselect != "" {
		sel.SelectByValue(preselect)
	}
	return sel
}

// runSelect runs the inline selector and returns the chosen value, or ok=false
// if the user canceled (Ctrl-C / Esc / q).
func runSelect(in io.Reader, out io.Writer, title string, items []components.SingleSelectItem, preselect string) (string, bool, error) {
	m := selectModel{title: title, sel: newSelect(items, preselect)}
	prog := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out))
	res, err := prog.Run()
	if err != nil {
		return "", false, err
	}
	final := res.(selectModel)
	if final.canceled {
		return "", false, nil
	}
	_, val := final.sel.Selected()
	return val, val != "", nil
}

// ChooseAgent asks the user to pick a coding agent. claudeAvailable controls the
// default highlight and description. Returns ok=false on cancel.
func ChooseAgent(in io.Reader, out io.Writer, claudeAvailable bool) (AgentChoice, bool, error) {
	claudeDesc := "Shell into Claude Code, wired with forge knowledge + tools"
	preselect := string(ChoiceClaude)
	if !claudeAvailable {
		claudeDesc = "Not found on PATH — install Claude Code, or choose forge native"
		preselect = string(ChoiceNative)
	}
	items := []components.SingleSelectItem{
		{Label: "Claude Code", Value: string(ChoiceClaude), Description: claudeDesc, Icon: "◆"},
		{Label: "forge (native)", Value: string(ChoiceNative), Description: "Build with forge's own in-process agent — no external tools needed", Icon: "⚒"},
	}
	val, ok, err := runSelect(in, out, "Code with:", items, preselect)
	if err != nil || !ok {
		return "", false, err
	}
	return AgentChoice(val), true, nil
}

// ChooseOptimizer asks whether to route Claude Code through the forge optimizer.
// It first prints the value proposition, then offers On/Off. The default is OFF
// unless a forge settings layer enabled it (defaultOn) — enabling the optimizer
// rewrites ANTHROPIC_BASE_URL, which can't be forced under enterprise-managed
// Claude Code, so it's opt-in via user/managed settings. Returns ok=false on cancel.
// enabled controls whether "On" is selectable: it's the first option always,
// but disabled (dimmed, unselectable) unless a forge settings layer enabled the
// optimizer — because turning it on rewrites ANTHROPIC_BASE_URL, which can't be
// forced under enterprise-managed Claude Code.
func ChooseOptimizer(in io.Reader, out io.Writer, color, enabled bool) (bool, bool, error) {
	printOptimizerPitch(out, color, enabled)
	// The On option always shows what it provides (cost optimization + memory);
	// when settings haven't enabled it, the label marks it disabled and it renders
	// unselectable. How to enable is explained in the pitch above the selector.
	onLabel := "On"
	if !enabled {
		onLabel = "On (disabled)"
	}
	items := []components.SingleSelectItem{
		{Label: onLabel, Value: "on", Description: "Cache-safe reversible compression to cut token cost + episodic & procedural memory", Disabled: !enabled},
		{Label: "Off", Value: "off", Description: "Plain Claude Code, no proxy"},
	}
	preselect := "off"
	if enabled {
		preselect = "on" // when allowed, default to On (its value proposition)
	}
	val, ok, err := runSelect(in, out, "Optimizer:", items, preselect)
	if err != nil || !ok {
		return false, false, err
	}
	return val == "on", true, nil
}

// printOptimizerPitch explains what the optimizer does before the selector.
func printOptimizerPitch(out io.Writer, color, defaultOn bool) {
	dim := dimFn(color)
	acc := accentFn(color)
	_, _ = fmt.Fprintf(out, "\n  %s\n", acc("The forge optimizer routes Claude Code through a local proxy that:"))
	_, _ = fmt.Fprintf(out, "    %s reversibly, cache-safely compresses context to cut token cost\n", acc("•"))
	_, _ = fmt.Fprintf(out, "    %s builds episodic + procedural memory from your sessions\n", acc("•"))
	_, _ = fmt.Fprintf(out, "  %s\n", dim("View token savings + memory at http://127.0.0.1:4200/#/optimizer  (run `forge ui`)."))
	if !defaultOn {
		_, _ = fmt.Fprintf(out, "  %s\n", dim("Optimizer is disabled for your org (it points ANTHROPIC_BASE_URL to a local Optimizer proxy before"))
		_, _ = fmt.Fprintf(out, "  %s\n", dim("forwarding the request to the enterprise-managed Anthropic Base URL). Enabling requires setting"))
		_, _ = fmt.Fprintf(out, "  %s\n", dim("ANTHROPIC_BASE_URL to the Optimizer local proxy in Claude Code managed settings, and the Upstream"))
		_, _ = fmt.Fprintf(out, "  %s\n", dim("Anthropic Base URL in Forge managed settings."))
	}
}

// PrintLaunching prints a one-line "launching X" notice before handing off.
func PrintLaunching(out io.Writer, color bool, msg string) {
	_, _ = fmt.Fprintf(out, "  %s\n\n", accentFn(color)("→ "+msg))
}
