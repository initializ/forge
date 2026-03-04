package steps

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

type channelPhase int

const (
	channelSelectPhase channelPhase = iota
	channelTokenPhase
	channelSlackBotTokenPhase
	channelDonePhase
)

// ChannelStep handles channel connector selection.
type ChannelStep struct {
	styles   *tui.StyleSet
	phase    channelPhase
	selector components.SingleSelect
	keyInput components.SecretInput
	complete bool
	channel  string
	tokens   map[string]string
}

// NewChannelStep creates a new channel step.
func NewChannelStep(styles *tui.StyleSet) *ChannelStep {
	items := []components.SingleSelectItem{
		{Label: "None", Value: "none", Description: "CLI / API only", Icon: "🚫"},
		{Label: "Telegram", Value: "telegram", Description: "Easy setup, no public URL needed", Icon: "✈️"},
		{Label: "Slack", Value: "slack", Description: "Socket Mode, no public URL needed", Icon: "💬"},
	}

	selector := components.NewSingleSelect(
		items,
		styles.Theme.Accent,
		styles.Theme.Primary,
		styles.Theme.Secondary,
		styles.Theme.Dim,
		styles.Theme.Border,
		styles.Theme.ActiveBorder,
		styles.Theme.ActiveBg,
		styles.KbdKey,
		styles.KbdDesc,
	)

	return &ChannelStep{
		styles:   styles,
		selector: selector,
		tokens:   make(map[string]string),
	}
}

func (s *ChannelStep) Title() string { return "Channel Connector" }
func (s *ChannelStep) Icon() string  { return "📡" }

func (s *ChannelStep) Init() tea.Cmd {
	return s.selector.Init()
}

func (s *ChannelStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	if wsm, ok := msg.(tea.WindowSizeMsg); ok && s.phase == channelSelectPhase {
		updated, cmd := s.selector.Update(wsm)
		s.selector = updated
		return s, cmd
	}

	switch s.phase {
	case channelSelectPhase:
		return s.updateSelectPhase(msg)
	case channelTokenPhase:
		return s.updateTokenPhase(msg)
	case channelSlackBotTokenPhase:
		return s.updateSlackBotTokenPhase(msg)
	}

	return s, nil
}

func (s *ChannelStep) updateSelectPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.selector.Update(msg)
	s.selector = updated

	if s.selector.Done() {
		_, val := s.selector.Selected()
		s.channel = val

		switch val {
		case "none":
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		case "telegram":
			s.phase = channelTokenPhase
			s.keyInput = components.NewSecretInput(
				"Telegram Bot Token (from @BotFather)",
				true, true,
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
			return s, s.keyInput.Init()
		case "slack":
			s.phase = channelTokenPhase
			s.keyInput = components.NewSecretInput(
				"Slack App Token (xapp-...)",
				true, true,
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
			return s, s.keyInput.Init()
		}
	}

	return s, cmd
}

func (s *ChannelStep) updateTokenPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if s.keyInput.Done() {
		val := s.keyInput.Value()

		switch s.channel {
		case "telegram":
			if val != "" {
				s.tokens["TELEGRAM_BOT_TOKEN"] = val
			}
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		case "slack":
			if val != "" {
				s.tokens["SLACK_APP_TOKEN"] = val
			}
			// Need bot token too
			s.phase = channelSlackBotTokenPhase
			s.keyInput = components.NewSecretInput(
				"Slack Bot Token (xoxb-...)",
				true, true,
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
			return s, s.keyInput.Init()
		}
	}

	return s, cmd
}

func (s *ChannelStep) updateSlackBotTokenPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if s.keyInput.Done() {
		if val := s.keyInput.Value(); val != "" {
			s.tokens["SLACK_BOT_TOKEN"] = val
		}
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, cmd
}

func (s *ChannelStep) View(width int) string {
	switch s.phase {
	case channelSelectPhase:
		return s.selector.View(width)
	case channelTokenPhase:
		var instructions string
		switch s.channel {
		case "telegram":
			instructions = fmt.Sprintf("  %s\n  %s\n  %s\n  %s\n\n",
				s.styles.SecondaryTxt.Render("Telegram Bot Setup:"),
				s.styles.DimTxt.Render("1. Open Telegram, message @BotFather"),
				s.styles.DimTxt.Render("2. Send /newbot and follow prompts"),
				s.styles.DimTxt.Render("3. Copy the bot token"),
			)
		case "slack":
			instructions = fmt.Sprintf("  %s\n  %s\n  %s\n  %s\n  %s\n  %s\n\n",
				s.styles.SecondaryTxt.Render("Slack App Setup (Step 1/2 — App-Level Token):"),
				s.styles.DimTxt.Render("1. Create a Slack App at https://api.slack.com/apps"),
				s.styles.DimTxt.Render("   → \"Create New App\" → \"From scratch\""),
				s.styles.DimTxt.Render("2. Settings → Socket Mode → toggle ON"),
				s.styles.DimTxt.Render("3. Basic Information → App-Level Tokens → Generate"),
				s.styles.DimTxt.Render("   → add scope: connections:write → copy the xapp-... token"),
			)
		}
		return instructions + s.keyInput.View(width)
	case channelSlackBotTokenPhase:
		botInstructions := fmt.Sprintf("  %s\n  %s\n  %s\n  %s\n  %s\n  %s\n  %s\n  %s\n\n",
			s.styles.SecondaryTxt.Render("Slack App Setup (Step 2/2 — Bot Token):"),
			s.styles.DimTxt.Render("4. Event Subscriptions → toggle ON → Subscribe to bot events:"),
			s.styles.DimTxt.Render("   → message.channels, message.im, app_mention"),
			s.styles.DimTxt.Render("5. OAuth & Permissions → Bot Token Scopes → add:"),
			s.styles.DimTxt.Render("   → app_mentions:read, chat:write, channels:history,"),
			s.styles.DimTxt.Render("     im:history, files:write, reactions:write"),
			s.styles.DimTxt.Render("6. Install App → Install to Workspace"),
			s.styles.DimTxt.Render("   → copy the xoxb-... Bot User OAuth Token"),
		)
		return botInstructions + s.keyInput.View(width)
	}
	return ""
}

func (s *ChannelStep) Complete() bool {
	return s.complete
}

func (s *ChannelStep) Summary() string {
	switch s.channel {
	case "none":
		return "None"
	case "telegram":
		return "Telegram"
	case "slack":
		return "Slack"
	}
	return s.channel
}

func (s *ChannelStep) Apply(ctx *tui.WizardContext) {
	ctx.Channel = s.channel
	for k, v := range s.tokens {
		ctx.ChannelTokens[k] = v
	}
}
