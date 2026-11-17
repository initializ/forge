package builtins

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/tools"
)

// suggestActionsTool lets the agent offer the user clickable follow-up actions
// (#562). It records the actions in the turn's ActionsCollector; the runtime
// attaches them to the response so interactive channels (e.g. Slack) render
// them as buttons. Choosing one re-enters the agent with that action's prompt
// as the user's next message (the synthesized-follow-up model). On channels
// without interactive components the reply text is delivered unchanged.
type suggestActionsTool struct{}

type suggestActionsInput struct {
	Actions []struct {
		Label  string `json:"label"`
		Prompt string `json:"prompt"`
	} `json:"actions"`
}

func (t *suggestActionsTool) Name() string { return "suggest_actions" }
func (t *suggestActionsTool) Description() string {
	return "Offer the user up to a few clickable follow-up actions, shown as buttons on channels " +
		"that support them (e.g. Slack). Each action has a short 'label' (the button text) and a " +
		"'prompt' — the message that is sent back as the user's next turn when they click it. Call " +
		"this when the next step is a small set of clear choices (e.g. 'Deploy to staging', 'Show the diff'). " +
		"The reply text is still shown; the actions are offered alongside it."
}
func (t *suggestActionsTool) Category() tools.Category { return tools.CategoryBuiltin }

func (t *suggestActionsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"actions": {
				"type": "array",
				"description": "The follow-up actions to offer (max 8).",
				"items": {
					"type": "object",
					"properties": {
						"label": {"type": "string", "description": "Button text shown to the user (max 75 chars)."},
						"prompt": {"type": "string", "description": "The message sent back as the user's next turn when this action is chosen (max 2000 chars)."}
					},
					"required": ["label", "prompt"]
				}
			}
		},
		"required": ["actions"]
	}`)
}

func (t *suggestActionsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input suggestActionsInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}
	if len(input.Actions) == 0 {
		return "", fmt.Errorf("actions must contain at least one {label, prompt}")
	}

	actions := make([]runtime.Action, 0, len(input.Actions))
	for _, a := range input.Actions {
		actions = append(actions, runtime.Action{Label: a.Label, Prompt: a.Prompt})
	}

	collector := runtime.ActionsCollectorFromContext(ctx)
	if collector == nil {
		// No collector in context (e.g. the tool ran outside an Execute turn).
		// Don't fail the call — just report that the actions won't be surfaced.
		return "No interactive surface available for this turn; actions were not attached.", nil
	}
	collector.Set(actions)

	// Report the accepted set (post-validation) so the model sees what will
	// actually be offered (empty labels/prompts dropped, capped at the max).
	accepted := collector.Actions()
	if len(accepted) == 0 {
		return "", fmt.Errorf("no valid actions: each needs a non-empty label and prompt")
	}
	labels := make([]string, len(accepted))
	for i, a := range accepted {
		labels[i] = a.Label
	}
	return fmt.Sprintf("Offered %d action(s) to the user: %v", len(accepted), labels), nil
}
