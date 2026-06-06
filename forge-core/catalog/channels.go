package catalog

// channels is the canonical, ordered list of messaging channel connectors.
var channels = []Channel{
	{
		ID:          "none",
		Label:       "None",
		Description: "CLI / API only",
		Icon:        "🚫",
	},
	{
		ID:          "telegram",
		Label:       "Telegram",
		Description: "Easy setup, no public URL needed",
		Icon:        "✈️",
		Credentials: []CredentialField{
			{EnvVar: "TELEGRAM_BOT_TOKEN", Prompt: "Telegram Bot Token (from @BotFather)", Secret: true},
		},
	},
	{
		ID:          "slack",
		Label:       "Slack",
		Description: "Socket Mode, no public URL needed",
		Icon:        "💬",
		Credentials: []CredentialField{
			{EnvVar: "SLACK_APP_TOKEN", Prompt: "Slack App Token (xapp-...)", Secret: true},
			{EnvVar: "SLACK_BOT_TOKEN", Prompt: "Slack Bot Token (xoxb-...)", Secret: true},
		},
	},
	{
		ID:          "msteams",
		Label:       "MS Teams",
		Description: "Graph polling, no public URL needed",
		Icon:        "👥",
		// MSTEAMS_REFRESH_TOKEN is obtained via an interactive device-code login
		// flow rather than a static prompt, so it is not listed here.
		Credentials: []CredentialField{
			{EnvVar: "MSTEAMS_TENANT_ID", Prompt: "MS Teams Tenant ID (GUID from Entra)", Secret: true},
			{EnvVar: "MSTEAMS_CLIENT_ID", Prompt: "MS Teams Client ID (GUID from Entra app)", Secret: true},
			{EnvVar: "MSTEAMS_CLIENT_SECRET", Prompt: "MS Teams Client Secret (from Entra app)", Secret: true},
		},
	},
}

// AllChannels returns the catalog of messaging channels in display order.
func AllChannels() []Channel { return channels }

// ChannelByID returns the channel with the given id, and whether it was found.
func ChannelByID(id string) (Channel, bool) {
	for _, c := range channels {
		if c.ID == id {
			return c, true
		}
	}
	return Channel{}, false
}
