package runtime

import "testing"

// suggest_actions is registered only when an interactive-capable channel is
// configured (#562, Option B), so it stays out of every other agent's tool
// context.
func TestInteractiveChannelConfigured(t *testing.T) {
	yes := [][]string{
		{"slack"},
		{"telegram", "slack"},
		{"SLACK"}, // case-insensitive
		{" slack "},
	}
	for _, chans := range yes {
		if !interactiveChannelConfigured(chans) {
			t.Errorf("interactiveChannelConfigured(%v) = false, want true", chans)
		}
	}
	no := [][]string{
		nil,
		{},
		{"telegram"},
		{"whatsapp", "teams"},
	}
	for _, chans := range no {
		if interactiveChannelConfigured(chans) {
			t.Errorf("interactiveChannelConfigured(%v) = true, want false", chans)
		}
	}
}
