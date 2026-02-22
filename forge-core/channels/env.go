package channels

import (
	"os"
	"strings"
)

// ResolveEnvVars inspects cfg.Settings for keys ending in "_env" and resolves
// them from the environment. For example, a setting "bot_token_env": "SLACK_BOT_TOKEN"
// produces {"bot_token": os.Getenv("SLACK_BOT_TOKEN")}.
// Non-env settings are passed through unchanged.
func ResolveEnvVars(cfg *ChannelConfig) map[string]string {
	resolved := make(map[string]string, len(cfg.Settings))
	for k, v := range cfg.Settings {
		if base, ok := strings.CutSuffix(k, "_env"); ok {
			resolved[base] = os.Getenv(v)
		} else {
			resolved[k] = v
		}
	}
	return resolved
}
