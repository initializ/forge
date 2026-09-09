package whatsapp

import "testing"

func TestSplitJID(t *testing.T) {
	tests := []struct {
		in         string
		wantUser   string
		wantServer string
	}{
		{"14155550100@s.whatsapp.net", "14155550100", "s.whatsapp.net"},
		{"120363000000000000@g.us", "120363000000000000", "g.us"},
		{"14155550100@S.WhatsApp.Net", "14155550100", "s.whatsapp.net"},
		{"bare", "bare", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		user, server := SplitJID(tt.in)
		if user != tt.wantUser || server != tt.wantServer {
			t.Errorf("SplitJID(%q) = (%q, %q), want (%q, %q)", tt.in, user, server, tt.wantUser, tt.wantServer)
		}
	}
}

// A device-qualified JID must compare equal to the bare one — the same person
// messaging from phone and desktop is one identity.
func TestJIDUser_StripsDeviceAndAgent(t *testing.T) {
	tests := map[string]string{
		"14155550100@s.whatsapp.net":     "14155550100",
		"14155550100:5@s.whatsapp.net":   "14155550100",
		"14155550100.0@s.whatsapp.net":   "14155550100",
		"14155550100.0:5@s.whatsapp.net": "14155550100",
	}
	for in, want := range tests {
		if got := JIDUser(in); got != want {
			t.Errorf("JIDUser(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeJID(t *testing.T) {
	tests := map[string]string{
		"14155550100:5@s.whatsapp.net": "14155550100@s.whatsapp.net",
		"14155550100@S.WHATSAPP.NET":   "14155550100@s.whatsapp.net",
		"120363000000000000@g.us":      "120363000000000000@g.us",
		"bare":                         "bare",
	}
	for in, want := range tests {
		if got := NormalizeJID(in); got != want {
			t.Errorf("NormalizeJID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJIDClassification(t *testing.T) {
	tests := []struct {
		jid                                            string
		group, newsletter, broadcast, user, lid, convo bool
	}{
		{jid: "14155550100@s.whatsapp.net", user: true, convo: true},
		{jid: "120363000000000000@g.us", group: true, convo: true},
		{jid: "123456@newsletter", newsletter: true},
		{jid: "status@broadcast", broadcast: true},
		{jid: "98765@lid", user: true, lid: true, convo: true},
	}
	for _, tt := range tests {
		if got := IsGroupJID(tt.jid); got != tt.group {
			t.Errorf("IsGroupJID(%q) = %v, want %v", tt.jid, got, tt.group)
		}
		if got := IsNewsletterJID(tt.jid); got != tt.newsletter {
			t.Errorf("IsNewsletterJID(%q) = %v, want %v", tt.jid, got, tt.newsletter)
		}
		if got := IsBroadcastJID(tt.jid); got != tt.broadcast {
			t.Errorf("IsBroadcastJID(%q) = %v, want %v", tt.jid, got, tt.broadcast)
		}
		if got := IsUserJID(tt.jid); got != tt.user {
			t.Errorf("IsUserJID(%q) = %v, want %v", tt.jid, got, tt.user)
		}
		if got := IsLIDJID(tt.jid); got != tt.lid {
			t.Errorf("IsLIDJID(%q) = %v, want %v", tt.jid, got, tt.lid)
		}
		if got := IsConversationJID(tt.jid); got != tt.convo {
			t.Errorf("IsConversationJID(%q) = %v, want %v", tt.jid, got, tt.convo)
		}
	}
}

func TestE164ToJID(t *testing.T) {
	tests := map[string]string{
		"+1 (415) 555-0100":          "14155550100@s.whatsapp.net",
		"1-415-555-0100":             "14155550100@s.whatsapp.net",
		"14155550100":                "14155550100@s.whatsapp.net",
		" +14155550100 ":             "14155550100@s.whatsapp.net",
		"14155550100@s.whatsapp.net": "14155550100@s.whatsapp.net",
		"":                           "",
		"not-a-number":               "",
	}
	for in, want := range tests {
		if got := E164ToJID(in); got != want {
			t.Errorf("E164ToJID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJIDToE164(t *testing.T) {
	tests := map[string]string{
		"14155550100@s.whatsapp.net":   "+14155550100",
		"14155550100:3@s.whatsapp.net": "+14155550100",
		"120363000000000000@g.us":      "", // groups carry no number
		"98765@lid":                    "", // hidden numbers carry no number
		"123@newsletter":               "",
	}
	for in, want := range tests {
		if got := JIDToE164(in); got != want {
			t.Errorf("JIDToE164(%q) = %q, want %q", in, got, want)
		}
	}
}

// Operators write allowlists by hand, so both punctuated numbers and raw JIDs
// have to land on the same key.
func TestParseJIDSet_SendersAcceptNumbersAndJIDs(t *testing.T) {
	set := parseJIDSet("+1 (415) 555-0100, 14155550199@s.whatsapp.net", serverUser)
	for _, want := range []string{"14155550100@s.whatsapp.net", "14155550199@s.whatsapp.net"} {
		if !set[want] {
			t.Errorf("expected %q in set, got %v", want, set)
		}
	}
	if len(set) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(set), set)
	}
}

func TestParseJIDSet_GroupsQualifyBareIDs(t *testing.T) {
	set := parseJIDSet("120363000000000000, 120363000000000001@g.us", serverGroup)
	for _, want := range []string{"120363000000000000@g.us", "120363000000000001@g.us"} {
		if !set[want] {
			t.Errorf("expected %q in set, got %v", want, set)
		}
	}
}

func TestParseJIDSet_Separators(t *testing.T) {
	set := parseJIDSet("14155550100,\n14155550101,14155550102", serverUser)
	if len(set) != 3 {
		t.Errorf("expected 3 entries across comma and newline, got %d: %v", len(set), set)
	}
}

// A space is part of a written phone number, not a separator — splitting on it
// would shred one entry into several bogus ones.
func TestParseJIDSet_SpaceIsNotASeparator(t *testing.T) {
	set := parseJIDSet("+1 (415) 555-0100", serverUser)
	if len(set) != 1 || !set["14155550100@s.whatsapp.net"] {
		t.Errorf("expected one entry for a spaced phone number, got %v", set)
	}
}

func TestParseJIDSet_Empty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n"} {
		if got := parseJIDSet(in, serverUser); len(got) != 0 {
			t.Errorf("parseJIDSet(%q) = %v, want empty", in, got)
		}
	}
}

// A device suffix in a config entry must not create a key the runtime lookup
// (which normalizes) can never hit.
func TestParseJIDSet_NormalizesDeviceSuffix(t *testing.T) {
	set := parseJIDSet("14155550100:7@s.whatsapp.net", serverUser)
	if !set["14155550100@s.whatsapp.net"] {
		t.Errorf("expected device suffix stripped, got %v", set)
	}
}
