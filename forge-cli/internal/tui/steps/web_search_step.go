package steps

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// ValidateWebSearchKeyFunc validates a web search API key for a given provider.
type ValidateWebSearchKeyFunc func(provider, key string) error

type webSearchPhase int

const (
	webSearchChoosePhase webSearchPhase = iota
	webSearchKeyPhase
	webSearchValidatingPhase
)

// WebSearchStep configures the built-in web_search tool: pick a provider
// (Tavily / Perplexity) or skip, and capture + validate its API key. Every
// builtin tool is auto-registered at runtime; web_search is the only one that
// needs operator input, so it gets a focused step of its own rather than being
// buried in a bulk tool checklist.
type WebSearchStep struct {
	styles     *tui.StyleSet
	phase      webSearchPhase
	choose     components.SingleSelect
	keyInput   components.SecretInput
	complete   bool
	validating bool

	provider   string // "tavily" or "perplexity"; "" when web search is disabled
	keyName    string // "TAVILY_API_KEY" or "PERPLEXITY_API_KEY"
	key        string
	keyFromEnv bool

	validateFn ValidateWebSearchKeyFunc
}

// NewWebSearchStep creates a new web search configuration step.
func NewWebSearchStep(styles *tui.StyleSet, validateFn ValidateWebSearchKeyFunc) *WebSearchStep {
	// Annotate a provider whose key is already in the environment so the user
	// knows selecting it will silently adopt that key (and picking the other
	// provider forgoes an already-available key). See #263 review.
	tavilyLabel := "Tavily (Recommended)"
	if os.Getenv("TAVILY_API_KEY") != "" {
		tavilyLabel += " · key detected in env"
	}
	perplexityLabel := "Perplexity"
	if os.Getenv("PERPLEXITY_API_KEY") != "" {
		perplexityLabel += " · key detected in env"
	}

	choose := components.NewSingleSelect(
		[]components.SingleSelectItem{
			{Label: tavilyLabel, Value: "tavily", Description: "LLM-optimized search with structured results", Icon: "🔍"},
			{Label: perplexityLabel, Value: "perplexity", Description: "AI-powered search with citations", Icon: "🌐"},
			{Label: "No web search", Value: "", Description: "Skip — agents have no live web access", Icon: "🚫"},
		},
		styles.Theme.Accent,
		styles.Theme.Primary,
		styles.Theme.Secondary,
		styles.Theme.Dim,
		styles.Theme.Border,
		styles.Theme.Accent,
		styles.Theme.AccentDim,
		styles.KbdKey,
		styles.KbdDesc,
	)

	return &WebSearchStep{
		styles:     styles,
		choose:     choose,
		validateFn: validateFn,
	}
}

func (s *WebSearchStep) Title() string { return "Web Search" }
func (s *WebSearchStep) Icon() string  { return "🔍" }

func (s *WebSearchStep) Init() tea.Cmd {
	// Reset on (re-)entry so BACK navigation restarts at the provider choice
	// instead of short-circuiting on a stale `s.complete`. Re-entry contract
	// — SkillsStep/CompressionStep precedent (#264 review).
	s.complete = false
	s.validating = false
	s.phase = webSearchChoosePhase
	return s.choose.Init()
}

func (s *WebSearchStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		switch s.phase {
		case webSearchChoosePhase:
			updated, cmd := s.choose.Update(wsm)
			s.choose = updated
			return s, cmd
		case webSearchKeyPhase:
			updated, cmd := s.keyInput.Update(wsm)
			s.keyInput = updated
			return s, cmd
		}
		return s, nil
	}

	switch s.phase {
	case webSearchChoosePhase:
		updated, cmd := s.choose.Update(msg)
		s.choose = updated

		if s.choose.Done() {
			_, s.provider = s.choose.Selected()

			// "No web search" — nothing more to configure.
			if s.provider == "" {
				return s.finish()
			}

			s.keyName = "TAVILY_API_KEY"
			if s.provider == "perplexity" {
				s.keyName = "PERPLEXITY_API_KEY"
			}

			// If the provider key is already in the environment, adopt it
			// without prompting.
			if os.Getenv(s.keyName) != "" {
				s.keyFromEnv = true
				return s.finish()
			}

			s.initKeyInput("")
			return s, s.keyInput.Init()
		}

		return s, cmd

	case webSearchKeyPhase:
		updated, cmd := s.keyInput.Update(msg)
		s.keyInput = updated

		if s.keyInput.Done() {
			s.key = s.keyInput.Value()

			if s.key != "" && s.validateFn != nil {
				s.phase = webSearchValidatingPhase
				s.validating = true
				return s, s.runValidation()
			}

			return s.finish()
		}

		return s, cmd

	case webSearchValidatingPhase:
		if msg, ok := msg.(tui.ValidationResultMsg); ok {
			s.validating = false
			if msg.Err != nil {
				// Validation failed — return to key input with the error.
				s.initKeyInput(fmt.Sprintf("retry — %s", msg.Err))
				s.keyInput.SetState(components.SecretInputFailed, msg.Err.Error())
				return s, s.keyInput.Init()
			}
			return s.finish()
		}
		return s, nil
	}

	return s, nil
}

func (s *WebSearchStep) finish() (tui.Step, tea.Cmd) {
	s.complete = true
	return s, func() tea.Msg { return tui.StepCompleteMsg{} }
}

// initKeyInput creates a fresh SecretInput for the provider API key.
func (s *WebSearchStep) initKeyInput(suffix string) {
	keyLabel := "Tavily API key for web_search"
	if s.provider == "perplexity" {
		keyLabel = "Perplexity API key for web_search"
	}
	if suffix != "" {
		keyLabel = fmt.Sprintf("%s (%s)", keyLabel, suffix)
	}

	s.phase = webSearchKeyPhase
	s.keyInput = components.NewSecretInput(
		keyLabel,
		false, true, // required — cannot skip; masked
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

// runValidation runs the web search key validation asynchronously.
func (s *WebSearchStep) runValidation() tea.Cmd {
	provider := s.provider
	key := s.key
	validateFn := s.validateFn
	return func() tea.Msg {
		if validateFn == nil {
			return tui.ValidationResultMsg{Err: nil}
		}
		return tui.ValidationResultMsg{Err: validateFn(provider, key)}
	}
}

func (s *WebSearchStep) View(width int) string {
	switch s.phase {
	case webSearchChoosePhase:
		return s.choose.View(width)
	case webSearchKeyPhase:
		return s.keyInput.View(width)
	case webSearchValidatingPhase:
		if s.validating {
			return "  " + s.styles.AccentTxt.Render("⣾ Validating...") + "\n"
		}
		return s.keyInput.View(width)
	}
	return ""
}

func (s *WebSearchStep) Complete() bool { return s.complete }

func (s *WebSearchStep) Summary() string {
	if s.provider == "" {
		return "disabled"
	}
	if s.keyFromEnv {
		return fmt.Sprintf("%s [key from env]", s.provider)
	}
	return s.provider
}

func (s *WebSearchStep) Apply(ctx *tui.WizardContext) {
	// Apply must write CURRENT state, not accumulate. Back-navigation makes
	// redo reachable: choosing a provider + key, then going back and picking
	// "No web search", must not leave the stale key / provider in the context
	// (they'd land in the generated .env for a tool the user dropped). Clear
	// the keys this step exclusively owns first, then re-write the current
	// selection. See #264 review.
	delete(ctx.EnvVars, "WEB_SEARCH_PROVIDER")
	delete(ctx.EnvVars, "TAVILY_API_KEY")
	delete(ctx.EnvVars, "PERPLEXITY_API_KEY")
	ctx.BuiltinTools = removeStr(ctx.BuiltinTools, "web_search")

	if s.provider == "" {
		return
	}
	// web_search is auto-registered; recording it in BuiltinTools keeps the
	// review/summary and any policy interaction consistent with the choice.
	ctx.BuiltinTools = append(ctx.BuiltinTools, "web_search")
	ctx.EnvVars["WEB_SEARCH_PROVIDER"] = s.provider
	if s.key != "" && s.keyName != "" {
		ctx.EnvVars[s.keyName] = s.key
	}
}

// removeStr returns slice with all occurrences of val removed, preserving
// order. Used so Apply can rewrite BuiltinTools idempotently on redo.
func removeStr(slice []string, val string) []string {
	out := slice[:0:0]
	for _, s := range slice {
		if s != val {
			out = append(out, s)
		}
	}
	return out
}
