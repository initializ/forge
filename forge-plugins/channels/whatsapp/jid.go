// Package whatsapp implements the WhatsApp channel plugin via the WhatsApp
// Web multidevice protocol (whatsmeow). It is a paired-session adapter — no
// inbound webhooks, no public endpoint, no bot token. Authentication is a QR
// pairing captured by `forge channel whatsapp-login` and persisted to a local
// session store.
package whatsapp

import (
	"strings"
)

// WhatsApp JID server suffixes. A JID is "<user>@<server>", where the server
// determines what kind of conversation it addresses.
const (
	// serverUser addresses an individual by phone number: "14155550100@s.whatsapp.net".
	serverUser = "s.whatsapp.net"
	// serverGroup addresses a group: "120363000000000000@g.us".
	serverGroup = "g.us"
	// serverNewsletter addresses a Channel (one-way broadcast feed). The agent
	// never participates in these.
	serverNewsletter = "newsletter"
	// serverBroadcast addresses a broadcast list or the status feed
	// ("status@broadcast"). Never a conversation the agent should answer.
	serverBroadcast = "broadcast"
	// serverLID addresses a user by their hidden-number identifier. WhatsApp
	// is migrating group participants to LIDs, so an inbound sender may arrive
	// as a LID with no phone number attached.
	serverLID = "lid"
)

// SplitJID separates a JID into its user and server parts. A JID with no "@"
// yields the whole string as the user and an empty server.
func SplitJID(jid string) (user, server string) {
	jid = strings.TrimSpace(jid)
	at := strings.LastIndex(jid, "@")
	if at < 0 {
		return jid, ""
	}
	return jid[:at], strings.ToLower(jid[at+1:])
}

// JIDServer returns the server portion of a JID, lowercased.
func JIDServer(jid string) string {
	_, server := SplitJID(jid)
	return server
}

// JIDUser returns the user portion of a JID with the device and agent
// suffixes removed.
//
// whatsmeow renders a specific linked device as "<user>:<device>@server" and
// newer builds add an agent ordinal as "<user>.<agent>". Neither belongs in an
// identity comparison — the same person messaging from phone and desktop must
// compare equal — so both are stripped.
func JIDUser(jid string) string {
	user, _ := SplitJID(jid)
	if i := strings.IndexByte(user, ':'); i >= 0 {
		user = user[:i]
	}
	if i := strings.IndexByte(user, '.'); i >= 0 {
		user = user[:i]
	}
	return user
}

// NormalizeJID reduces a JID to its comparable form: lowercased server, and a
// user with device and agent suffixes stripped. Use it before any equality
// check or allowlist lookup.
func NormalizeJID(jid string) string {
	server := JIDServer(jid)
	if server == "" {
		return JIDUser(jid)
	}
	return JIDUser(jid) + "@" + server
}

// IsGroupJID reports whether the JID addresses a group chat.
func IsGroupJID(jid string) bool { return JIDServer(jid) == serverGroup }

// IsNewsletterJID reports whether the JID addresses a Channel / newsletter.
func IsNewsletterJID(jid string) bool { return JIDServer(jid) == serverNewsletter }

// IsBroadcastJID reports whether the JID addresses a broadcast list or the
// status feed.
func IsBroadcastJID(jid string) bool { return JIDServer(jid) == serverBroadcast }

// IsUserJID reports whether the JID addresses an individual, by either phone
// number or hidden-number LID.
func IsUserJID(jid string) bool {
	switch JIDServer(jid) {
	case serverUser, serverLID:
		return true
	}
	return false
}

// IsLIDJID reports whether the JID is a hidden-number identifier rather than a
// phone number. A LID sender has no E.164 form, so an allowlist keyed on phone
// numbers cannot match it.
func IsLIDJID(jid string) bool { return JIDServer(jid) == serverLID }

// IsConversationJID reports whether the JID addresses something the agent may
// hold a conversation in — a DM or a group. Newsletters, broadcast lists and
// the status feed are excluded: they are one-way feeds, and replying to one
// either fails or fans a message out to every contact.
func IsConversationJID(jid string) bool {
	return IsGroupJID(jid) || IsUserJID(jid)
}

// E164ToJID builds a user JID from a phone number in any common written form
// ("+1 (415) 555-0100", "1-415-555-0100", "14155550100"). Returns "" when the
// input holds no digits.
//
// An input that already looks like a JID is normalized and returned as-is, so
// operators may write either form in an allowlist.
func E164ToJID(number string) string {
	number = strings.TrimSpace(number)
	if strings.Contains(number, "@") {
		return NormalizeJID(number)
	}
	digits := digitsOnly(number)
	if digits == "" {
		return ""
	}
	return digits + "@" + serverUser
}

// JIDToE164 renders a user JID as a "+"-prefixed phone number. Returns "" for
// a group, newsletter, broadcast or LID JID — none of which carry a number.
func JIDToE164(jid string) string {
	if !IsUserJID(jid) || IsLIDJID(jid) {
		return ""
	}
	digits := digitsOnly(JIDUser(jid))
	if digits == "" {
		return ""
	}
	return "+" + digits
}

// digitsOnly strips every non-digit rune. Used to make written phone numbers
// comparable regardless of the punctuation an operator typed.
func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseJIDSet builds a lookup set from a comma- or newline-separated config
// list. Entries may be written as JIDs or as bare phone numbers; both
// normalize to the same key, so a caller looks up with NormalizeJID.
//
// Group IDs have no phone-number form, so a bare group id ("120363...") is
// accepted and qualified with the group server.
//
// Note the separator set is narrower than the Teams adapter's
// parseAllowBotIDs, which also splits on spaces. A space is legitimate INSIDE
// a written phone number ("+1 (415) 555-0100"), so treating it as a separator
// would shred one entry into four bogus ones.
func parseJIDSet(s string, defaultServer string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}) {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "@") {
			if defaultServer == serverUser {
				if jid := E164ToJID(entry); jid != "" {
					out[jid] = true
				}
				continue
			}
			entry += "@" + defaultServer
		}
		out[NormalizeJID(entry)] = true
	}
	return out
}
