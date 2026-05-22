package msteams

import (
	"strings"

	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

// AdmitMode is the inbound message gating policy.
type AdmitMode string

const (
	// AdmitMention drops every message except those that @-mention the agent.
	AdmitMention AdmitMode = "mention"
	// AdmitDM drops every message except 1:1 chat messages.
	AdmitDM AdmitMode = "dm"
	// AdmitMentionOrDM admits messages that satisfy either condition.
	AdmitMentionOrDM AdmitMode = "mention_or_dm"
)

// admissionResult is the verdict the gate returns to the caller. When admit
// is false, reason is the structured log line the caller should emit at
// DEBUG so operators can diagnose silent drops.
type admissionResult struct {
	admit  bool
	reason string
}

// admit applies the admission gate. The order matters:
//
//  1. Bot admission — non-user authors must be in allowBotIDs.
//  2. Mode filter — mention / dm / mention_or_dm.
//
// Loop prevention is NOT done here via from.user.id comparison. In
// delegated mode, the agent's outbound messages have the same from.user.id
// as user-typed messages (delegated tokens act AS the user) — there's no
// way to tell them apart inside admit(). The Plugin instead pre-populates
// the dedup ring with every outbound message ID it sends, so polling
// drops those before reaching admission. ownUserID is kept on the
// signature for diagnostic / future use but is not consulted as a gate.
//
// Dedup is the caller's responsibility (it has different lock granularity
// and is needed for ALL paths, not just admitted ones).
func admit(
	msg *ChatMessage,
	ownUserID string,
	allowBotIDs map[string]bool,
	mode AdmitMode,
	chatType string,
) admissionResult {
	_ = ownUserID // intentionally unused; see comment above

	// 1. Bot admission.
	if msg.From != nil && msg.From.Application != nil {
		botID := msg.From.Application.ID
		if !allowBotIDs[botID] {
			return admissionResult{
				admit:  false,
				reason: "msteams: dropping bot message (bot_id=" + botID + "); add to msteams-config.yaml allow_bot_ids to admit",
			}
		}
	}

	// 3. Mode filter.
	isDM := chatType == "oneOnOne"
	isMentioned := markdown.ExtractMention(msg.Body.Content, msg.Mentions, ownUserID)

	switch mode {
	case AdmitMention:
		if !isMentioned {
			return admissionResult{admit: false, reason: "msteams: dropping non-mention message (admit=mention)"}
		}
	case AdmitDM:
		if !isDM {
			return admissionResult{admit: false, reason: "msteams: dropping non-dm message (admit=dm)"}
		}
	case AdmitMentionOrDM:
		if !isMentioned && !isDM {
			return admissionResult{admit: false, reason: "msteams: dropping non-mention non-dm message (admit=mention_or_dm)"}
		}
	default:
		// Unknown mode — default to mention_or_dm semantics.
		if !isMentioned && !isDM {
			return admissionResult{admit: false, reason: "msteams: dropping message under default mention_or_dm gate"}
		}
	}

	return admissionResult{admit: true}
}

// stripBotMention removes the literal "@DisplayName" prefix that Teams
// renders for a mention, so the prompt sent to the LLM doesn't include the
// agent's own name as the first word. Case-insensitive, only matches at the
// start (after optional whitespace).
func stripBotMention(text, displayName string) string {
	if displayName == "" {
		return text
	}
	trimmed := strings.TrimSpace(text)
	prefix := "@" + displayName
	if !strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(prefix)) {
		return text
	}
	stripped := strings.TrimSpace(trimmed[len(prefix):])
	// Drop a leading punctuation like ":" or "," after the mention.
	stripped = strings.TrimLeft(stripped, ":, ")
	return stripped
}
