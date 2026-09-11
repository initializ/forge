package whatsapp

import (
	"strings"
	"testing"
)

const (
	testDM     = "14155550100@s.whatsapp.net"
	testGroup  = "120363000000000000@g.us"
	testSender = "14155550100@s.whatsapp.net"
	testOwn    = "14155550999@s.whatsapp.net"
)

const testOwnJID = "14155550999@s.whatsapp.net"

// defaultCfg is the open-sender baseline most of these cases assume. The
// shipped default is owner-only; ownerOnlyCfg covers that.
func defaultCfg() admissionConfig {
	return admissionConfig{
		Mode:           AdmitDMOrGroupMention,
		AllowAnySender: true,
		SelfChat:       true,
		OwnJIDs:        []string{testOwnJID},
	}
}

// ownerOnlyCfg is the shipped default: no allowlist entries, no opt-out.
func ownerOnlyCfg() admissionConfig {
	return admissionConfig{
		Mode:     AdmitDMOrGroupMention,
		SelfChat: true,
		OwnJIDs:  []string{testOwnJID},
	}
}

func TestAdmit_DMAdmittedByDefault(t *testing.T) {
	got := admit(testDM, testSender, "", false, false, defaultCfg())
	if !got.admit {
		t.Errorf("expected DM admitted, got drop: %s", got.reason)
	}
}

func TestAdmit_GroupMentionAdmittedByDefault(t *testing.T) {
	got := admit(testGroup, testSender, "", false, true, defaultCfg())
	if !got.admit {
		t.Errorf("expected mentioned group message admitted, got drop: %s", got.reason)
	}
}

func TestAdmit_GroupWithoutMentionDropped(t *testing.T) {
	got := admit(testGroup, testSender, "", false, false, defaultCfg())
	if got.admit {
		t.Error("expected non-mention group message dropped")
	}
	if !strings.Contains(got.reason, "non-mention") {
		t.Errorf("reason should name the gate, got %q", got.reason)
	}
}

// The agent answering its own output recurses until the server rate-limits it.
func TestAdmit_SelfLoopDropped(t *testing.T) {
	got := admit(testDM, testOwn, "", true, false, defaultCfg())
	if got.admit {
		t.Error("expected own outbound message dropped")
	}
	if !strings.Contains(got.reason, "is_from_me") {
		t.Errorf("reason should name the self-loop guard, got %q", got.reason)
	}
}

// Replying into a newsletter or the status feed either fails or fans out to
// every contact — neither is ever admissible.
func TestAdmit_NonConversationJIDsDropped(t *testing.T) {
	for _, chat := range []string{"123@newsletter", "status@broadcast", "list@broadcast"} {
		got := admit(chat, testSender, "", false, true, defaultCfg())
		if got.admit {
			t.Errorf("expected %q dropped", chat)
		}
		if !strings.Contains(got.reason, "non-conversation") {
			t.Errorf("reason for %q should name the kind gate, got %q", chat, got.reason)
		}
	}
}

func TestAdmit_ModeDM_DropsGroups(t *testing.T) {
	cfg := admissionConfig{Mode: AdmitDM, AllowAnySender: true, OwnJIDs: []string{testOwnJID}}
	if got := admit(testGroup, testSender, "", false, true, cfg); got.admit {
		t.Error("expected group dropped under admit=dm even when mentioned")
	}
	if got := admit(testDM, testSender, "", false, false, cfg); !got.admit {
		t.Errorf("expected DM admitted under admit=dm, got drop: %s", got.reason)
	}
}

func TestAdmit_ModeGroupMention_DropsDMs(t *testing.T) {
	cfg := admissionConfig{Mode: AdmitGroupMention, AllowAnySender: true, OwnJIDs: []string{testOwnJID}}
	if got := admit(testDM, testSender, "", false, false, cfg); got.admit {
		t.Error("expected DM dropped under admit=group_mention")
	}
	if got := admit(testGroup, testSender, "", false, true, cfg); !got.admit {
		t.Errorf("expected mentioned group message admitted, got drop: %s", got.reason)
	}
	if got := admit(testGroup, testSender, "", false, false, cfg); got.admit {
		t.Error("expected non-mention group message dropped under admit=group_mention")
	}
}

// An unrecognised mode must not open the gate wider than the default.
func TestAdmit_UnknownModeFallsBackToDefault(t *testing.T) {
	cfg := admissionConfig{Mode: AdmitMode("nonsense"), AllowAnySender: true, OwnJIDs: []string{testOwnJID}}
	if got := admit(testGroup, testSender, "", false, false, cfg); got.admit {
		t.Error("expected unknown mode to keep the non-mention group gate closed")
	}
	if got := admit(testDM, testSender, "", false, false, cfg); !got.admit {
		t.Errorf("expected unknown mode to still admit DMs, got drop: %s", got.reason)
	}
}

func TestAdmit_SenderAllowlist(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowAnySender = false
	cfg.AllowedSenders = parseJIDSet("14155550100", serverUser)

	if got := admit(testDM, "14155550100@s.whatsapp.net", "", false, false, cfg); !got.admit {
		t.Errorf("expected listed sender admitted, got drop: %s", got.reason)
	}
	if got := admit(testDM, "14155550777@s.whatsapp.net", "", false, false, cfg); got.admit {
		t.Error("expected unlisted sender dropped")
	}
}

// A sender on a second linked device must still match a bare-number entry.
func TestAdmit_SenderAllowlistIgnoresDeviceSuffix(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowAnySender = false
	cfg.AllowedSenders = parseJIDSet("14155550100", serverUser)
	if got := admit(testDM, "14155550100:4@s.whatsapp.net", "", false, false, cfg); !got.admit {
		t.Errorf("expected device-qualified sender admitted, got drop: %s", got.reason)
	}
}

// A phone-number allowlist can never match a hidden-number sender. Fail closed
// with a reason that says so, rather than one that reads like a plain miss.
func TestAdmit_LIDSenderFailsClosedWithExplanation(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowAnySender = false
	cfg.AllowedSenders = parseJIDSet("14155550100", serverUser)
	got := admit(testGroup, "98765@lid", "", false, true, cfg)
	if got.admit {
		t.Error("expected LID sender dropped against a number-keyed allowlist")
	}
	if !strings.Contains(got.reason, "lid") {
		t.Errorf("reason should explain the LID mismatch, got %q", got.reason)
	}
}

// A group that has migrated to hidden numbers reports a LID sender with the
// phone number in SenderAlt. An operator who wrote the allowlist in phone
// numbers must not be locked out by that migration.
func TestAdmit_LIDSenderMatchesViaPhoneAlternate(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowAnySender = false
	cfg.AllowedSenders = parseJIDSet("14155550100", serverUser)
	got := admit(testGroup, "98765@lid", "14155550100@s.whatsapp.net", false, true, cfg)
	if !got.admit {
		t.Errorf("expected LID sender admitted via phone alternate, got drop: %s", got.reason)
	}
}

// The reverse migration: allowlist written in LIDs, sender arrives by number.
func TestAdmit_PhoneSenderMatchesViaLIDAlternate(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowAnySender = false
	cfg.AllowedSenders = parseJIDSet("98765@lid", serverUser)
	got := admit(testGroup, "14155550100@s.whatsapp.net", "98765@lid", false, true, cfg)
	if !got.admit {
		t.Errorf("expected sender admitted via LID alternate, got drop: %s", got.reason)
	}
}

// An alternate that is itself unlisted must not widen the gate.
func TestAdmit_UnlistedAlternateStillDropped(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowAnySender = false
	cfg.AllowedSenders = parseJIDSet("14155550100", serverUser)
	got := admit(testGroup, "98765@lid", "14155550777@s.whatsapp.net", false, true, cfg)
	if got.admit {
		t.Error("expected drop when neither identity is listed")
	}
}

// With no sender allowlist configured, a LID sender is ordinary traffic.
func TestAdmit_LIDSenderAdmittedWithoutAllowlist(t *testing.T) {
	if got := admit(testGroup, "98765@lid", "", false, true, defaultCfg()); !got.admit {
		t.Errorf("expected LID sender admitted with no allowlist, got drop: %s", got.reason)
	}
}

func TestAdmit_GroupAllowlist(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowedGroups = parseJIDSet("120363000000000000", serverGroup)

	if got := admit(testGroup, testSender, "", false, true, cfg); !got.admit {
		t.Errorf("expected listed group admitted, got drop: %s", got.reason)
	}
	if got := admit("120363000000000009@g.us", testSender, "", false, true, cfg); got.admit {
		t.Error("expected unlisted group dropped")
	}
}

// The group allowlist gates groups only; it must not silently block DMs.
func TestAdmit_GroupAllowlistDoesNotAffectDMs(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowedGroups = parseJIDSet("120363000000000000", serverGroup)
	if got := admit(testDM, testSender, "", false, false, cfg); !got.admit {
		t.Errorf("expected DM admitted despite group allowlist, got drop: %s", got.reason)
	}
}

func TestIsMentioned(t *testing.T) {
	own := "14155550999@s.whatsapp.net"

	if !isMentioned([]string{"14155550100@s.whatsapp.net", own}, own) {
		t.Error("expected mention detected")
	}
	if isMentioned([]string{"14155550100@s.whatsapp.net"}, own) {
		t.Error("expected no mention when own JID absent")
	}
	if isMentioned(nil, own) {
		t.Error("expected no mention for empty list")
	}
	if isMentioned([]string{own}) {
		t.Error("expected no mention when no own JID supplied")
	}
}

// The mention list holds bare JIDs while the paired account is device-
// qualified; comparison has to normalize both sides.
func TestIsMentioned_NormalizesBothSides(t *testing.T) {
	if !isMentioned([]string{"14155550999@s.whatsapp.net"}, "14155550999:12@s.whatsapp.net") {
		t.Error("expected device-qualified own JID to match a bare mention")
	}
}

// A LID-migrated group reports the agent's LID, not its number — both
// identities must be accepted.
func TestIsMentioned_MatchesEitherIdentity(t *testing.T) {
	if !isMentioned([]string{"55555@lid"}, "14155550999@s.whatsapp.net", "55555@lid") {
		t.Error("expected LID mention to match the agent's LID identity")
	}
}

// --- self-chat (the personal-agent flow) ---

// The owner's "Message Yourself" chat: the chat JID is the owner's own, and
// the prompt arrives with IsFromMe set. This is the whole feature.
func TestAdmit_SelfChatAcceptsOwnMessage(t *testing.T) {
	got := admit(testOwnJID, testOwnJID, "", true, false, ownerOnlyCfg())
	if !got.admit {
		t.Errorf("expected own message admitted in the self-chat, got drop: %s", got.reason)
	}
}

func TestAdmit_SelfChatDisabledDropsOwnMessage(t *testing.T) {
	cfg := ownerOnlyCfg()
	cfg.SelfChat = false
	got := admit(testOwnJID, testOwnJID, "", true, false, cfg)
	if got.admit {
		t.Error("expected own message dropped when self_chat is off")
	}
}

// Outside the self-chat an own message is our echo. Answering it recurses.
func TestAdmit_OwnMessageElsewhereStillDropped(t *testing.T) {
	cfg := ownerOnlyCfg()
	for _, chat := range []string{testDM, testGroup} {
		if got := admit(chat, testOwnJID, "", true, true, cfg); got.admit {
			t.Errorf("expected own message dropped in %q", chat)
		}
	}
}

// A group is never a self-chat, even one the owner created.
func TestAdmit_SelfChatNeverAppliesToGroups(t *testing.T) {
	cfg := ownerOnlyCfg()
	if cfg.isSelfChat(testGroup) {
		t.Error("a group must never count as the self-chat")
	}
}

// The owner's LID is also their identity, so the self-chat must resolve
// under it too.
func TestAdmit_SelfChatMatchesLIDIdentity(t *testing.T) {
	cfg := ownerOnlyCfg()
	cfg.OwnJIDs = []string{testOwnJID, "55555@lid"}
	if got := admit("55555@lid", "55555@lid", "", true, false, cfg); !got.admit {
		t.Errorf("expected self-chat under the LID identity admitted, got drop: %s", got.reason)
	}
}

// --- owner-only default ---

// The shipped default must not let a stranger with the number reach the agent.
func TestAdmit_EmptyAllowlistMeansOwnerOnly(t *testing.T) {
	cfg := ownerOnlyCfg()

	if got := admit(testDM, "14155550777@s.whatsapp.net", "", false, false, cfg); got.admit {
		t.Error("expected a stranger dropped under the owner-only default")
	}
	if got := admit(testDM, testOwnJID, "", false, false, cfg); !got.admit {
		t.Errorf("expected the owner admitted, got drop: %s", got.reason)
	}
}

// The drop reason must explain the default, not read like a bare miss.
func TestAdmit_OwnerOnlyReasonIsActionable(t *testing.T) {
	got := admit(testDM, "14155550777@s.whatsapp.net", "", false, false, ownerOnlyCfg())
	for _, want := range []string{"owner-only", "allowed_senders"} {
		if !strings.Contains(got.reason, want) {
			t.Errorf("reason should mention %q, got %q", want, got.reason)
		}
	}
}

func TestAdmit_AllowAnySenderOpensItUp(t *testing.T) {
	cfg := ownerOnlyCfg()
	cfg.AllowAnySender = true
	if got := admit(testDM, "14155550777@s.whatsapp.net", "", false, false, cfg); !got.admit {
		t.Errorf("expected any sender admitted with the opt-out, got drop: %s", got.reason)
	}
}

// Listing others must not lock the owner out of their own agent.
func TestAdmit_OwnerPassesEvenWhenNotListed(t *testing.T) {
	cfg := ownerOnlyCfg()
	cfg.AllowedSenders = parseJIDSet("14155550100", serverUser)
	if got := admit(testDM, testOwnJID, "", false, false, cfg); !got.admit {
		t.Errorf("owner must always pass, got drop: %s", got.reason)
	}
}

func TestAdmit_OwnerMatchedViaAlternateIdentity(t *testing.T) {
	cfg := ownerOnlyCfg()
	if got := admit(testGroup, "99999@lid", testOwnJID, false, true, cfg); !got.admit {
		t.Errorf("owner should be recognised via SenderAlt, got drop: %s", got.reason)
	}
}

func TestIsAnySender(t *testing.T) {
	for _, s := range []string{"anyone", "any", "*", " ANYONE ", "Any"} {
		if !isAnySender(s) {
			t.Errorf("isAnySender(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "  ", "+14155550100", "anyone@s.whatsapp.net"} {
		if isAnySender(s) {
			t.Errorf("isAnySender(%q) = true, want false", s)
		}
	}
}
