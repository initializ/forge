package steps

import (
	"context"
	"fmt"
	"net/http"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/devicecode"
	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"

	"github.com/initializ/forge/forge-core/catalog"
)

type channelPhase int

const (
	channelSelectPhase channelPhase = iota
	channelTokenPhase
	channelSlackBotTokenPhase
	channelMsteamsClientIDPhase
	channelMsteamsClientSecretPhase
	channelMsteamsDeviceLoginPhase
	channelDonePhase
)

// msteams device-login sub-states inside channelMsteamsDeviceLoginPhase.
type msteamsLoginStatus int

const (
	msteamsLoginRequesting msteamsLoginStatus = iota // POST /devicecode in flight
	msteamsLoginWaiting                              // showing URL + code, polling /token
	msteamsLoginErr                                  // last attempt failed; show retry/skip
)

// Tea messages produced by the device-code goroutines. They flow through the
// wizard's main loop and are routed back to ChannelStep.Update.
type msteamsDeviceCodeReadyMsg struct {
	dc  *devicecode.DeviceCodeResponse
	err error
}
type msteamsRefreshTokenReadyMsg struct {
	token string
	err   error
}

// ChannelStep handles channel connector selection.
type ChannelStep struct {
	styles   *tui.StyleSet
	phase    channelPhase
	selector components.SingleSelect
	keyInput components.SecretInput
	complete bool

	// msteams device-login state. Populated when the user reaches the
	// device-login phase via steps 1-3 (tenant / client / secret).
	loginStatus msteamsLoginStatus
	loginDevice *devicecode.DeviceCodeResponse
	loginErr    string
	channel     string
	tokens      map[string]string
}

// channelSelectItems projects the catalog channels into TUI select items.
func channelSelectItems() []components.SingleSelectItem {
	cs := catalog.AllChannels()
	items := make([]components.SingleSelectItem, 0, len(cs))
	for _, c := range cs {
		items = append(items, components.SingleSelectItem{
			Label: c.Label, Value: c.ID, Description: c.Description, Icon: c.Icon,
		})
	}
	return items
}

// NewChannelStep creates a new channel step.
func NewChannelStep(styles *tui.StyleSet) *ChannelStep {
	items := channelSelectItems()

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
		// Last secret-input step. After capturing client_secret, transition
		// into the inline device-login phase which runs the OAuth flow
		// itself instead of asking the operator to paste a refresh token.
		return s.updateMsteamsClientSecretPhase(msg)
	case channelMsteamsDeviceLoginPhase:
		return s.updateMsteamsDeviceLoginPhase(msg)
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

// updateMsteamsPhase is the shared advance logic for the intermediate msteams
// secret-input chain (tenant_id → client_id → client_secret). On Done, stores
// the just-collected value under storeKey and moves to nextPhase with the
// prompt nextPrompt. Only valid for the three intermediate phases — the
// terminal device-login phase uses updateMsteamsDeviceLoginPhase instead.
func (s *ChannelStep) updateMsteamsPhase(msg tea.Msg, storeKey, nextPrompt string, nextPhase channelPhase) (tui.Step, tea.Cmd) {
	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if !s.keyInput.Done() {
		return s, cmd
	}

	if val := s.keyInput.Value(); val != "" {
		s.tokens[storeKey] = val
	}

	s.phase = nextPhase
	s.keyInput = s.newMsteamsInput(nextPrompt)
	return s, s.keyInput.Init()
}

// updateMsteamsClientSecretPhase captures MSTEAMS_CLIENT_SECRET and then
// transitions into the inline device-login phase. Unlike the earlier secret
// inputs, this one does NOT show another text-input prompt afterward —
// instead it kicks off the OAuth device-code flow directly.
func (s *ChannelStep) updateMsteamsClientSecretPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if !s.keyInput.Done() {
		return s, cmd
	}

	if val := s.keyInput.Value(); val != "" {
		s.tokens["MSTEAMS_CLIENT_SECRET"] = val
	}

	// Enter the device-login phase. We immediately request a device code.
	s.phase = channelMsteamsDeviceLoginPhase
	s.loginStatus = msteamsLoginRequesting
	s.loginErr = ""
	return s, s.requestDeviceCodeCmd()
}

// updateMsteamsDeviceLoginPhase is the state machine for the inline OAuth
// device-code flow. It handles the two async events (device code received,
// refresh token received) plus the retry/skip key presses available in
// the error state.
func (s *ChannelStep) updateMsteamsDeviceLoginPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	switch m := msg.(type) {
	case msteamsDeviceCodeReadyMsg:
		if m.err != nil {
			s.loginStatus = msteamsLoginErr
			s.loginErr = m.err.Error()
			return s, nil
		}
		s.loginDevice = m.dc
		s.loginStatus = msteamsLoginWaiting
		// Best-effort browser open. Failures are silently ignored — the
		// verification URL is also rendered in the View so the operator
		// can navigate manually if the open fails.
		_ = devicecode.OpenURL(m.dc.VerificationURI)
		return s, s.pollTokenCmd(m.dc)

	case msteamsRefreshTokenReadyMsg:
		if m.err != nil {
			s.loginStatus = msteamsLoginErr
			s.loginErr = m.err.Error()
			return s, nil
		}
		if m.token != "" {
			s.tokens["MSTEAMS_REFRESH_TOKEN"] = m.token
		}
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }

	case tea.KeyMsg:
		if s.loginStatus != msteamsLoginErr {
			return s, nil
		}
		switch m.String() {
		case "r", "R":
			// Retry — request a fresh device code.
			s.loginStatus = msteamsLoginRequesting
			s.loginErr = ""
			return s, s.requestDeviceCodeCmd()
		case "s", "S":
			// Skip — finish the wizard with no refresh token. The agent
			// can still be started later after the operator captures the
			// token out-of-band with `forge channel msteams-login --write-env`.
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}
	}

	return s, nil
}

// requestDeviceCodeCmd kicks off RequestDeviceCode against the tenant/client
// the operator just provided. Returns a tea.Cmd that produces a
// msteamsDeviceCodeReadyMsg on completion.
func (s *ChannelStep) requestDeviceCodeCmd() tea.Cmd {
	tenant := s.tokens["MSTEAMS_TENANT_ID"]
	clientID := s.tokens["MSTEAMS_CLIENT_ID"]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dc, err := devicecode.RequestDeviceCode(ctx, &http.Client{Timeout: 30 * time.Second},
			devicecode.DefaultLoginBase, tenant, clientID)
		return msteamsDeviceCodeReadyMsg{dc: dc, err: err}
	}
}

// pollTokenCmd kicks off PollDeviceToken. Returns a tea.Cmd that produces a
// msteamsRefreshTokenReadyMsg on completion. Uses a generous 15-minute
// timeout — matches Microsoft's device-code expiry.
//
// We pass the client_secret captured in step 3 because confidential-client
// (web) Entra app registrations return AADSTS7000218 if /token is called
// without it. Public-client (native) apps ignore the extra parameter, so
// always passing it is safe.
func (s *ChannelStep) pollTokenCmd(dc *devicecode.DeviceCodeResponse) tea.Cmd {
	tenant := s.tokens["MSTEAMS_TENANT_ID"]
	clientID := s.tokens["MSTEAMS_CLIENT_ID"]
	clientSecret := s.tokens["MSTEAMS_CLIENT_SECRET"]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		tok, err := devicecode.PollDeviceToken(ctx, &http.Client{Timeout: 30 * time.Second},
			devicecode.DefaultLoginBase, tenant, clientID, clientSecret, dc)
		if err != nil {
			return msteamsRefreshTokenReadyMsg{err: err}
		}
		if tok.RefreshToken == "" {
			return msteamsRefreshTokenReadyMsg{err: fmt.Errorf("token endpoint returned no refresh_token — did the scope include offline_access?")}
		}
		return msteamsRefreshTokenReadyMsg{token: tok.RefreshToken}
	}
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
	case channelMsteamsDeviceLoginPhase:
		return s.viewMsteamsDeviceLogin()
	}
	return ""
}

// viewMsteamsDeviceLogin renders the three sub-states of the inline OAuth
// device-code flow. No SecretInput is shown — the operator's only action
// is to complete sign-in in the browser (the URL is already open).
func (s *ChannelStep) viewMsteamsDeviceLogin() string {
	switch s.loginStatus {
	case msteamsLoginRequesting:
		return fmt.Sprintf("  %s\n  %s\n\n",
			s.styles.SecondaryTxt.Render("MS Teams Setup (Step 4/4 — Sign in to Microsoft):"),
			s.styles.AccentTxt.Render("⣾ Requesting a one-time code from Microsoft..."),
		)

	case msteamsLoginWaiting:
		dc := s.loginDevice
		return fmt.Sprintf("  %s\n\n  %s\n  %s\n\n  %s\n  %s\n\n  %s\n  %s\n  %s\n\n  %s\n  %s\n\n",
			s.styles.SecondaryTxt.Render("MS Teams Setup (Step 4/4 — Sign in to Microsoft):"),
			s.styles.DimTxt.Render("Your browser should have just opened. If not, go to:"),
			s.styles.AccentTxt.Render("    "+dc.VerificationURI),
			s.styles.DimTxt.Render("Enter this one-time code when prompted:"),
			s.styles.AccentTxt.Render("    "+dc.UserCode),
			s.styles.ErrorTxt.Render("IMPORTANT: sign in as the dedicated Microsoft 365 account"),
			s.styles.ErrorTxt.Render("you want the agent to ACT AS (e.g. forge-agent@yourtenant)."),
			s.styles.DimTxt.Render("Other Teams users will @-mention that account to invoke the agent."),
			s.styles.DimTxt.Render("⣾ Waiting for you to complete sign-in..."),
			s.styles.DimTxt.Render("(This page will advance automatically once you approve.)"),
		)

	case msteamsLoginErr:
		return fmt.Sprintf("  %s\n\n  %s\n  %s\n\n  %s\n  %s\n",
			s.styles.SecondaryTxt.Render("MS Teams Setup (Step 4/4 — Sign in to Microsoft):"),
			s.styles.ErrorTxt.Render("✗ Device-code flow failed:"),
			s.styles.DimTxt.Render("    "+s.loginErr),
			s.styles.DimTxt.Render("Press R to retry, or S to skip and capture the refresh token"),
			s.styles.DimTxt.Render("later with `forge channel msteams-login --write-env`."),
		)
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
