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
	channelMsteamsClientIDPhase
	channelMsteamsClientSecretPhase
	channelMsteamsRefreshTokenPhase
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
		{Label: "MS Teams", Value: "msteams", Description: "Graph polling, no public URL needed", Icon: "👥"},
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
	case channelMsteamsClientIDPhase:
		return s.updateMsteamsPhase(msg, "MSTEAMS_CLIENT_ID",
			"MS Teams Client Secret (from Entra app)", channelMsteamsClientSecretPhase)
	case channelMsteamsClientSecretPhase:
		return s.updateMsteamsPhase(msg, "MSTEAMS_CLIENT_SECRET",
			"MS Teams Refresh Token (from device-code flow)", channelMsteamsRefreshTokenPhase)
	case channelMsteamsRefreshTokenPhase:
		return s.updateMsteamsPhase(msg, "MSTEAMS_REFRESH_TOKEN", "", channelDonePhase)
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
		case "msteams":
			// 4-step flow: tenant_id → client_id → client_secret → refresh_token.
			// The refresh token is captured externally via the device-code flow
			// documented in printSetupInstructions (forge-cli/cmd/channel.go).
			s.phase = channelTokenPhase
			s.keyInput = s.newMsteamsInput("MS Teams Tenant ID (GUID from Entra)")
			return s, s.keyInput.Init()
		}
	}

	return s, cmd
}

// newMsteamsInput builds a SecretInput with the standard theme bindings used
// by the rest of this step. Centralised so the 4 msteams phases stay terse.
func (s *ChannelStep) newMsteamsInput(label string) components.SecretInput {
	return components.NewSecretInput(
		label,
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
		case "msteams":
			// First msteams field captured: MSTEAMS_TENANT_ID.
			// Hand off to the 3-stage chain via the shared helper.
			if val != "" {
				s.tokens["MSTEAMS_TENANT_ID"] = val
			}
			s.phase = channelMsteamsClientIDPhase
			s.keyInput = s.newMsteamsInput("MS Teams Client ID (GUID from Entra app)")
			return s, s.keyInput.Init()
		}
	}

	return s, cmd
}

// updateMsteamsPhase is the shared advance logic for the msteams token chain.
// Each phase stores the just-collected value under storeKey, and either
// transitions to the next phase (with the prompt nextPrompt) or marks the step
// complete when nextPhase == channelDonePhase.
func (s *ChannelStep) updateMsteamsPhase(msg tea.Msg, storeKey, nextPrompt string, nextPhase channelPhase) (tui.Step, tea.Cmd) {
	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if !s.keyInput.Done() {
		return s, cmd
	}

	if val := s.keyInput.Value(); val != "" {
		s.tokens[storeKey] = val
	}

	if nextPhase == channelDonePhase {
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	s.phase = nextPhase
	s.keyInput = s.newMsteamsInput(nextPrompt)
	return s, s.keyInput.Init()
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
		case "msteams":
			instructions = fmt.Sprintf("  %s\n  %s\n  %s\n  %s\n  %s\n  %s\n\n",
				s.styles.SecondaryTxt.Render("MS Teams Setup (Step 1/4 — Tenant ID):"),
				s.styles.DimTxt.Render("1. Register an Entra ID app at https://entra.microsoft.com"),
				s.styles.DimTxt.Render("2. Add delegated API permissions: Chat.Read, Chat.ReadWrite, User.Read"),
				s.styles.DimTxt.Render("3. Grant admin consent if your tenant requires it"),
				s.styles.DimTxt.Render("4. Overview tab → copy the Directory (tenant) ID — paste below"),
				s.styles.DimTxt.Render("   (outbound polling only — no public URL required)"),
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
	case channelMsteamsClientIDPhase:
		ins := fmt.Sprintf("  %s\n  %s\n  %s\n\n",
			s.styles.SecondaryTxt.Render("MS Teams Setup (Step 2/4 — Client ID):"),
			s.styles.DimTxt.Render("Entra app → Overview → Application (client) ID."),
			s.styles.DimTxt.Render("Same page as the tenant ID; paste it below."),
		)
		return ins + s.keyInput.View(width)
	case channelMsteamsClientSecretPhase:
		ins := fmt.Sprintf("  %s\n  %s\n  %s\n  %s\n\n",
			s.styles.SecondaryTxt.Render("MS Teams Setup (Step 3/4 — Client Secret):"),
			s.styles.DimTxt.Render("Entra app → Certificates & secrets → New client secret."),
			s.styles.DimTxt.Render("Copy the Value (not the Secret ID) immediately —"),
			s.styles.DimTxt.Render("Entra only shows it once."),
		)
		return ins + s.keyInput.View(width)
	case channelMsteamsRefreshTokenPhase:
		ins := fmt.Sprintf("  %s\n  %s\n  %s\n  %s\n  %s\n  %s\n  %s\n\n",
			s.styles.SecondaryTxt.Render("MS Teams Setup (Step 4/4 — Refresh Token):"),
			s.styles.DimTxt.Render("Capture a refresh token via the device-code flow:"),
			s.styles.DimTxt.Render("  curl -X POST \"https://login.microsoftonline.com/$TENANT/oauth2/v2.0/devicecode\" \\"),
			s.styles.DimTxt.Render("    -d \"client_id=$CLIENT_ID\" \\"),
			s.styles.DimTxt.Render("    -d \"scope=https://graph.microsoft.com/.default offline_access\""),
			s.styles.DimTxt.Render("Visit verification_uri, enter user_code, then exchange device_code"),
			s.styles.DimTxt.Render("at /token (grant_type=device_code) to receive a refresh_token."),
		)
		return ins + s.keyInput.View(width)
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
	case "msteams":
		return "MS Teams"
	}
	return s.channel
}

func (s *ChannelStep) Apply(ctx *tui.WizardContext) {
	ctx.Channel = s.channel
	for k, v := range s.tokens {
		ctx.ChannelTokens[k] = v
	}
}
