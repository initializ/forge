package whatsapp

// AdmitMode is the inbound message gating policy.
type AdmitMode string

const (
	// AdmitDM admits only 1:1 chat messages; every group message is dropped.
	AdmitDM AdmitMode = "dm"
	// AdmitGroupMention admits only group messages that @-mention the agent.
	AdmitGroupMention AdmitMode = "group_mention"
	// AdmitDMOrGroupMention admits 1:1 messages and @-mentions in groups. This
	// is the default: an agent sitting in a group must not answer every line
	// of an unrelated human conversation.
	AdmitDMOrGroupMention AdmitMode = "dm_or_group_mention"
)

// admissionConfig is the resolved gating policy, built once at Init.
type admissionConfig struct {
	Mode AdmitMode
	// AllowedGroups restricts which groups the agent acts in. Empty = every
	// group, still subject to Mode.
	AllowedGroups map[string]bool
	// AllowedSenders restricts who may invoke the agent. Empty means OWNER
	// ONLY: the paired account is the sole permitted sender. Anyone with the
	// number can otherwise reach the agent, spend its LLM budget and use
	// whatever it can access, so the closed default is the safe one.
	// AllowAnySender opens it up explicitly.
	AllowedSenders map[string]bool
	// AllowAnySender disables the sender allowlist entirely (`allowed_senders:
	// anyone`). Deliberately verbose: it is the setting that exposes the agent
	// to the whole world.
	AllowAnySender bool
	// SelfChat accepts messages the owner sends in their own "Message
	// Yourself" chat, so the paired phone can talk to its own agent without a
	// second account.
	SelfChat bool
	// OwnJIDs are the paired account's identities (phone JID, and LID when
	// known). Used to recognise the self-chat and the owner as a sender.
	OwnJIDs []string
}

// isOwner reports whether either of a sender's identities is the paired
// account.
func (c admissionConfig) isOwner(senderJID, senderAlt string) bool {
	for _, own := range c.OwnJIDs {
		if own == "" {
			continue
		}
		n := NormalizeJID(own)
		if n == NormalizeJID(senderJID) || (senderAlt != "" && n == NormalizeJID(senderAlt)) {
			return true
		}
	}
	return false
}

// isSelfChat reports whether chatJID is the owner's own chat — WhatsApp's
// "Message Yourself" conversation, whose JID is the account's own.
func (c admissionConfig) isSelfChat(chatJID string) bool {
	if IsGroupJID(chatJID) {
		return false
	}
	for _, own := range c.OwnJIDs {
		if own != "" && NormalizeJID(own) == NormalizeJID(chatJID) {
			return true
		}
	}
	return false
}

// admissionResult is the verdict the gate returns to the caller. When admit
// is false, reason is the structured log line the caller should emit at DEBUG
// so operators can diagnose silent drops.
type admissionResult struct {
	admit  bool
	reason string
}

// admit applies the admission gate to one inbound message. The order matters:
//
//  1. Conversation kind — newsletters, broadcast lists and the status feed are
//     never answerable.
//  2. Self-loop — our own outbound echo.
//  3. Sender allowlist.
//  4. Group allowlist.
//  5. Mode filter.
//
// Dedup is the caller's responsibility: it must run for ALL inbound messages,
// not only admitted ones, or a re-delivered message that was dropped the first
// time can slip through on a later pass.
//
// Unlike the Teams adapter, the self-loop check here is authoritative.
// whatsmeow sets IsFromMe on anything sent by the paired account, including
// messages this adapter sent, so it distinguishes the agent's own output from
// a human's on the same account.
//
// senderAlt is whatsmeow's alternate address for the sender (MessageSource.
// SenderAlt): a phone-number sender carries its LID there and a LID sender
// carries its phone number. Both are checked against the allowlist so a group
// that has migrated to hidden numbers doesn't lock out an operator who wrote
// the list in phone numbers. Pass "" when there is no alternate.
func admit(chatJID, senderJID, senderAlt string, isFromMe, mentioned bool, cfg admissionConfig) admissionResult {
	// 1. Conversation kind.
	if !IsConversationJID(chatJID) {
		return admissionResult{
			admit:  false,
			reason: "whatsapp: dropping non-conversation message (chat=" + chatJID + "); newsletters, broadcast lists and status are not answerable",
		}
	}

	selfChat := cfg.isSelfChat(chatJID)

	// 2. Own messages.
	//
	// In the owner's self-chat these are the whole point: the paired phone
	// talks to its own agent, so IsFromMe is how the prompt arrives. The
	// agent's OWN replies are also IsFromMe there, and the loop guard for
	// those is the dedup ring, which marks every outbound id before
	// SendResponse returns and runs before this gate. Anywhere else, an own
	// message is our echo and answering it would recurse until rate-limited.
	if isFromMe {
		if !cfg.SelfChat || !selfChat {
			return admissionResult{admit: false, reason: "whatsapp: dropping own outbound message (is_from_me)"}
		}
	}

	// 3. Sender allowlist. The owner always passes — in the self-chat there is
	// no one else, and locking the paired account out of its own agent would
	// be nonsense.
	if !cfg.AllowAnySender && !cfg.isOwner(senderJID, senderAlt) {
		if len(cfg.AllowedSenders) == 0 {
			return admissionResult{
				admit:  false,
				reason: "whatsapp: dropping non-owner sender (" + senderJID + ") — allowed_senders is empty, which means owner-only; list the number, or set allowed_senders: anyone to open the agent up",
			}
		}
		if !senderAllowed(senderJID, senderAlt, cfg.AllowedSenders) {
			if IsLIDJID(senderJID) && senderAlt == "" {
				return admissionResult{
					admit:  false,
					reason: "whatsapp: dropping hidden-number sender (lid=" + senderJID + ") — allowed_senders is keyed on phone numbers and no phone-number alternate was supplied; add the LID itself to admit this sender",
				}
			}
			return admissionResult{
				admit:  false,
				reason: "whatsapp: dropping sender not in allowed_senders (sender=" + senderJID + ")",
			}
		}
	}

	isGroup := IsGroupJID(chatJID)

	// 4. Group allowlist.
	if isGroup && len(cfg.AllowedGroups) > 0 && !cfg.AllowedGroups[NormalizeJID(chatJID)] {
		return admissionResult{
			admit:  false,
			reason: "whatsapp: dropping group not in allowed_groups (chat=" + chatJID + ")",
		}
	}

	// 5. Mode filter.
	switch cfg.Mode {
	case AdmitDM:
		if isGroup {
			return admissionResult{admit: false, reason: "whatsapp: dropping group message (admit=dm)"}
		}
	case AdmitGroupMention:
		if !isGroup {
			return admissionResult{admit: false, reason: "whatsapp: dropping dm (admit=group_mention)"}
		}
		if !mentioned {
			return admissionResult{admit: false, reason: "whatsapp: dropping non-mention group message (admit=group_mention)"}
		}
	case AdmitDMOrGroupMention:
		if isGroup && !mentioned {
			return admissionResult{admit: false, reason: "whatsapp: dropping non-mention group message (admit=dm_or_group_mention)"}
		}
	default:
		// Unknown mode — fall back to dm_or_group_mention semantics rather
		// than admitting everything.
		if isGroup && !mentioned {
			return admissionResult{admit: false, reason: "whatsapp: dropping non-mention group message under default dm_or_group_mention gate"}
		}
	}

	return admissionResult{admit: true}
}

// senderAllowed reports whether either of a sender's identities is listed.
func senderAllowed(senderJID, senderAlt string, allowed map[string]bool) bool {
	if allowed[NormalizeJID(senderJID)] {
		return true
	}
	return senderAlt != "" && allowed[NormalizeJID(senderAlt)]
}

// isMentioned reports whether ownJID appears in a message's mention list.
//
// WhatsApp carries mentions out-of-band in contextInfo.mentionedJid rather
// than as markup in the body, so this is an exact JID-set membership test, not
// a text scan. Comparison is on the normalized JID because the mention list
// holds bare user JIDs while the paired account may be device-qualified.
func isMentioned(mentionedJIDs []string, ownJIDs ...string) bool {
	if len(mentionedJIDs) == 0 {
		return false
	}
	own := make(map[string]bool, len(ownJIDs))
	for _, j := range ownJIDs {
		if j = NormalizeJID(j); j != "" {
			own[j] = true
		}
	}
	if len(own) == 0 {
		return false
	}
	for _, m := range mentionedJIDs {
		if own[NormalizeJID(m)] {
			return true
		}
	}
	return false
}
