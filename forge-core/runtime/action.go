package runtime

import (
	"context"
	"strings"
	"sync"
)

// MetadataKeyActions is the a2a.Message.Metadata key under which the runtime
// attaches channel-neutral follow-up Actions (#562). Channel adapters that
// support interactive components (e.g. Slack Block Kit buttons) render them;
// others ignore the key and deliver the text reply unchanged. The value is a
// []Action, but readers should use ActionsFromMetadata, which also tolerates
// the []any-of-maps shape a JSON round-trip produces.
const MetadataKeyActions = "forge.actions"

// Action is a channel-neutral follow-up the agent offers the user — rendered as
// a clickable button where the channel supports it. Label is the button text;
// Prompt is the message sent back AS THE USER's next turn when the action is
// chosen (the synthesized-follow-up model: a click re-enters the agent exactly
// as if the user had typed Prompt). Deliberately transport-agnostic so every
// channel can render it its own way (Slack buttons, Telegram inline keyboard,
// a numbered text fallback, …).
type Action struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

// Action bounds. Label maps to a Slack button label (75-char limit); Prompt
// maps to a button value (2000-char limit). MaxActions caps a single offer so a
// turn can't emit an unwieldy or abusive number of buttons.
const (
	MaxActionLabelLen  = 75
	MaxActionPromptLen = 2000
	MaxActions         = 8
)

// ActionsCollector accumulates the follow-up Actions a turn offers (via the
// suggest_actions builtin). One is seeded into the Execute context so the tool
// can write and finalizeResponse can read within the same turn. Safe for
// concurrent tool execution.
type ActionsCollector struct {
	mu      sync.Mutex
	actions []Action
}

// NewActionsCollector returns an empty collector.
func NewActionsCollector() *ActionsCollector { return &ActionsCollector{} }

// Set replaces the collected actions (the latest suggest_actions call wins, so
// a re-offer within a turn supersedes an earlier one rather than appending).
// Invalid/empty entries are dropped and the set is capped at MaxActions.
func (c *ActionsCollector) Set(actions []Action) {
	if c == nil {
		return
	}
	cleaned := make([]Action, 0, len(actions))
	for _, a := range actions {
		a.Label = strings.TrimSpace(a.Label)
		a.Prompt = strings.TrimSpace(a.Prompt)
		if a.Label == "" || a.Prompt == "" {
			continue
		}
		if len(a.Label) > MaxActionLabelLen {
			a.Label = a.Label[:MaxActionLabelLen]
		}
		if len(a.Prompt) > MaxActionPromptLen {
			a.Prompt = a.Prompt[:MaxActionPromptLen]
		}
		cleaned = append(cleaned, a)
		if len(cleaned) >= MaxActions {
			break
		}
	}
	c.mu.Lock()
	c.actions = cleaned
	c.mu.Unlock()
}

// Actions returns a copy of the collected actions.
func (c *ActionsCollector) Actions() []Action {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.actions) == 0 {
		return nil
	}
	out := make([]Action, len(c.actions))
	copy(out, c.actions)
	return out
}

type actionsCollectorKey struct{}

// WithActionsCollector seeds an ActionsCollector into ctx. Execute calls this at
// the start of a turn so the suggest_actions tool and finalizeResponse share one.
func WithActionsCollector(ctx context.Context, c *ActionsCollector) context.Context {
	return context.WithValue(ctx, actionsCollectorKey{}, c)
}

// ActionsCollectorFromContext returns the turn's collector, or nil if none was
// seeded (so callers no-op gracefully outside an Execute turn).
func ActionsCollectorFromContext(ctx context.Context) *ActionsCollector {
	c, _ := ctx.Value(actionsCollectorKey{}).(*ActionsCollector)
	return c
}

// ActionsFromMetadata decodes the Actions attached to an a2a message's metadata
// under MetadataKeyActions. It tolerates both the native []Action (in-process
// delivery) and the []any-of-maps shape a JSON round-trip yields (remote/
// persisted messages). Returns nil when absent or malformed.
func ActionsFromMetadata(meta map[string]any) []Action {
	if meta == nil {
		return nil
	}
	switch v := meta[MetadataKeyActions].(type) {
	case []Action:
		return v
	case []any:
		out := make([]Action, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			label, _ := m["label"].(string)
			prompt, _ := m["prompt"].(string)
			if label == "" || prompt == "" {
				continue
			}
			out = append(out, Action{Label: label, Prompt: prompt})
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}
