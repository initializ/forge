package runtime

import (
	"context"
	"strings"

	"github.com/initializ/forge/forge-core/llm"
)

// summaryInlineThreshold is the response length (in characters) above which
// the runtime asks the LLM for a short summary that channel adapters can use
// instead of head-truncating the verbose response.
const summaryInlineThreshold = 4096

// summaryPrompt is the system instruction used for the one-shot summariser
// call. The model receives the verbose response as the user message and must
// return only the summary text.
const summaryPrompt = `You are summarising an agent's response so a chat channel (Slack, Telegram, etc.) can show a brief, useful preview while the full text is attached as a file. Write 2-4 sentences (or 3-5 bullets if the response is structured) that convey the answer or conclusion — not the introduction. Do not say "this report" or "the response". Do not add any preamble. Plain markdown only.`

// generateSummary asks the LLM for a short summary of the given response text.
// Returns an empty string on any error or empty completion — callers fall back
// to head-truncation in that case. The summariser is best-effort and never
// fails the request.
func generateSummary(ctx context.Context, client llm.Client, response string) string {
	if client == nil || strings.TrimSpace(response) == "" {
		return ""
	}
	resp, err := client.Chat(ctx, &llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: llm.RoleSystem, Content: summaryPrompt},
			{Role: llm.RoleUser, Content: response},
		},
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(resp.Message.Content)
}
