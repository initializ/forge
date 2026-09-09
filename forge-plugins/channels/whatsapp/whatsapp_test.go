package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
)

func cfgWith(settings map[string]string) channels.ChannelConfig {
	return channels.ChannelConfig{Adapter: "whatsapp", Settings: settings}
}

func TestPlugin_Name(t *testing.T) {
	if got := New().Name(); got != "whatsapp" {
		t.Errorf("got %q, want %q", got, "whatsapp")
	}
}

func TestInit_Defaults(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.cfg.SessionPath != defaultSessionPath {
		t.Errorf("session path = %q, want %q", p.cfg.SessionPath, defaultSessionPath)
	}
	if p.cfg.Admission.Mode != AdmitDMOrGroupMention {
		t.Errorf("admit = %q, want %q", p.cfg.Admission.Mode, AdmitDMOrGroupMention)
	}
	if !p.cfg.IncludeRecentHistory || p.history == nil {
		t.Error("expected history enabled by default")
	}
	if p.cfg.RecentHistoryCount != 20 {
		t.Errorf("history count = %d, want 20", p.cfg.RecentHistoryCount)
	}
}

func TestInit_ParsesSettings(t *testing.T) {
	p := New()
	err := p.Init(cfgWith(map[string]string{
		"session_path":           "/tmp/x/session.db",
		"admit":                  "dm",
		"allowed_groups":         "120363000000000000",
		"allowed_senders":        "+1 (415) 555-0100",
		"include_recent_history": "false",
		"recent_history_count":   "5",
	}))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.cfg.SessionPath != "/tmp/x/session.db" {
		t.Errorf("session path = %q", p.cfg.SessionPath)
	}
	if p.cfg.Admission.Mode != AdmitDM {
		t.Errorf("admit = %q", p.cfg.Admission.Mode)
	}
	if !p.cfg.Admission.AllowedGroups["120363000000000000@g.us"] {
		t.Errorf("groups = %v", p.cfg.Admission.AllowedGroups)
	}
	if !p.cfg.Admission.AllowedSenders["14155550100@s.whatsapp.net"] {
		t.Errorf("senders = %v", p.cfg.Admission.AllowedSenders)
	}
	if p.cfg.IncludeRecentHistory || p.history != nil {
		t.Error("expected history disabled")
	}
}

// A typo in admit must fail loudly at Init rather than silently degrade to a
// different gate at runtime.
func TestInit_RejectsUnknownAdmitMode(t *testing.T) {
	err := New().Init(cfgWith(map[string]string{"admit": "everything"}))
	if err == nil {
		t.Fatal("expected error for unknown admit mode")
	}
	if !strings.Contains(err.Error(), "admit must be one of") {
		t.Errorf("error should list valid modes, got %v", err)
	}
}

func TestInit_RejectsNegativeHistoryCount(t *testing.T) {
	err := New().Init(cfgWith(map[string]string{"recent_history_count": "-1"}))
	if err == nil {
		t.Fatal("expected error for negative history count")
	}
}

// Settings resolve through the shared _env indirection, so a session path can
// come from the environment like every other adapter's config.
func TestInit_ResolvesEnvSuffix(t *testing.T) {
	t.Setenv("TEST_WA_SESSION", "/tmp/from-env.db")
	p := New()
	if err := p.Init(cfgWith(map[string]string{"session_path_env": "TEST_WA_SESSION"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.cfg.SessionPath != "/tmp/from-env.db" {
		t.Errorf("session path = %q, want the env value", p.cfg.SessionPath)
	}
}

// Pairing needs a human scanning a QR code, so a server boot must fail with an
// actionable message rather than hang waiting for one.
func TestStart_FailsClosedWithoutSession(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(map[string]string{
		"session_path": filepath.Join(t.TempDir(), "absent.db"),
	})); err != nil {
		t.Fatalf("Init: %v", err)
	}

	err := p.Start(context.Background(), nil)
	if err == nil {
		t.Fatal("expected Start to fail on an unpaired session")
	}
	if !strings.Contains(err.Error(), "whatsapp-login") {
		t.Errorf("error should name the pairing command, got %v", err)
	}
}

func TestSessionExists_FalseWhenAbsent(t *testing.T) {
	if SessionExists(context.Background(), filepath.Join(t.TempDir(), "absent.db")) {
		t.Error("expected false for a missing session file")
	}
}

// A store that opens but holds no completed pairing is not a usable session.
func TestSessionExists_FalseForUnpairedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	ctx := context.Background()

	container, device, err := NewSessionDevice(ctx, path)
	if err != nil {
		t.Fatalf("NewSessionDevice: %v", err)
	}
	defer container.Close() //nolint:errcheck
	if device == nil {
		t.Fatal("expected a device to pair")
	}
	if device.ID != nil {
		t.Error("a fresh device must not be pre-paired")
	}
	if SessionExists(ctx, path) {
		t.Error("expected false for a store with no completed pairing")
	}
}

// The store must be creatable under a directory that doesn't exist yet —
// .forge/channels/ is not present in a fresh project.
func TestNewSessionDevice_CreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "session.db")
	container, _, err := NewSessionDevice(context.Background(), path)
	if err != nil {
		t.Fatalf("NewSessionDevice: %v", err)
	}
	defer container.Close() //nolint:errcheck
}

// The store holds the pairing — a world-readable credential file is a leak.
func TestNewSessionDevice_SessionFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	container, _, err := NewSessionDevice(context.Background(), path)
	if err != nil {
		t.Fatalf("NewSessionDevice: %v", err)
	}
	defer container.Close() //nolint:errcheck

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session file mode = %04o, want 0600", perm)
	}
}

func TestOpenContainer_CreatesDirOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	container, err := openContainer(context.Background(), filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatalf("openContainer: %v", err)
	}
	defer container.Close() //nolint:errcheck

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("session dir mode = %04o, want 0700", perm)
	}
}

func TestOpenContainer_RejectsEmptyPath(t *testing.T) {
	if _, err := openContainer(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty session path")
	}
}

func TestSessionDSN_UsesModerncPragmaSyntax(t *testing.T) {
	dsn := sessionDSN("/tmp/x.db")
	// mattn's "_foreign_keys=on" is silently ignored by modernc; the pragma
	// form is what actually enables cascading deletes.
	if !strings.Contains(dsn, "_pragma=foreign_keys(1)") {
		t.Errorf("dsn missing the modernc foreign-keys pragma: %q", dsn)
	}
	if strings.Contains(dsn, "_foreign_keys=") {
		t.Errorf("dsn uses the mattn pragma form, which modernc ignores: %q", dsn)
	}
}

func TestExtractMessageText(t *testing.T) {
	tests := []struct {
		name string
		msg  *waE2E.Message
		want string
	}{
		{"nil", nil, ""},
		{"conversation", &waE2E.Message{Conversation: proto.String("plain")}, "plain"},
		{
			"extended text",
			&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("with context")}},
			"with context",
		},
		{
			"image caption",
			&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("what is this?")}},
			"what is this?",
		},
		{
			"document caption",
			&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: proto.String("summarise")}},
			"summarise",
		},
		{"empty message", &waE2E.Message{}, ""},
	}
	for _, tt := range tests {
		if got := extractMessageText(tt.msg); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

// An edited message arrives wrapped; the new text is what the agent should see.
func TestExtractMessageText_UnwrapsEdit(t *testing.T) {
	msg := &waE2E.Message{
		EditedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{Conversation: proto.String("corrected")},
		},
	}
	if got := extractMessageText(msg); got != "corrected" {
		t.Errorf("got %q, want %q", got, "corrected")
	}
}

func TestMentionedJIDs(t *testing.T) {
	if got := mentionedJIDs(nil); got != nil {
		t.Errorf("nil message: got %v", got)
	}
	if got := mentionedJIDs(&waE2E.Message{Conversation: proto.String("hi")}); got != nil {
		t.Errorf("plain conversation carries no mentions: got %v", got)
	}

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("@agent hi"),
			ContextInfo: &waE2E.ContextInfo{
				MentionedJID: []string{"14155550999@s.whatsapp.net"},
			},
		},
	}
	got := mentionedJIDs(msg)
	if len(got) != 1 || got[0] != "14155550999@s.whatsapp.net" {
		t.Errorf("got %v", got)
	}
}

func TestExtractText_A2A(t *testing.T) {
	if got := extractText(nil); got != "(no response)" {
		t.Errorf("nil: got %q", got)
	}
	if got := extractText(&a2a.Message{}); got != "(no text response)" {
		t.Errorf("empty parts: got %q", got)
	}

	msg := &a2a.Message{Parts: []a2a.Part{
		{Kind: a2a.PartKindText, Text: "line one"},
		{Kind: a2a.PartKindText, Text: "line two"},
	}}
	if got := extractText(msg); got != "line one\nline two" {
		t.Errorf("got %q", got)
	}
}

// ctxzip markers are internal artifacts; a user must never see one.
func TestExtractText_StripsCompressionMarkers(t *testing.T) {
	msg := &a2a.Message{Parts: []a2a.Part{
		{Kind: a2a.PartKindText, Text: "before <<ctxzip:abcdef123456 note>> after"},
	}}
	if got := extractText(msg); strings.Contains(got, "ctxzip") {
		t.Errorf("marker leaked: %q", got)
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		in  string
		def bool
		out bool
	}{
		{"", true, true},
		{"", false, false},
		{"true", false, true},
		{"false", true, false},
		{"1", false, true},
		{"garbage", true, true},
	}
	for _, tt := range tests {
		if got := parseBool(tt.in, tt.def); got != tt.out {
			t.Errorf("parseBool(%q, %v) = %v, want %v", tt.in, tt.def, got, tt.out)
		}
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		in  string
		def int
		out int
	}{
		{"", 20, 20},
		{"5", 20, 5},
		{"garbage", 20, 20},
		{" 7 ", 20, 7},
	}
	for _, tt := range tests {
		if got := parseInt(tt.in, tt.def); got != tt.out {
			t.Errorf("parseInt(%q, %d) = %d, want %d", tt.in, tt.def, got, tt.out)
		}
	}
}

func TestStop_IsIdempotent(t *testing.T) {
	p := New()
	if err := p.Stop(); err != nil {
		t.Errorf("first Stop: %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestMarkSent_IgnoresEmptyID(t *testing.T) {
	p := New()
	p.markSent("")
	if p.dedup.size() != 0 {
		t.Errorf("expected empty id ignored, size = %d", p.dedup.size())
	}
	p.markSent("real")
	if !p.dedup.seen("real") {
		t.Error("expected real id recorded")
	}
}

// --- history-replay guard ---

// After a restart the dedup ring is empty, so a replayed conversation would be
// answered from scratch — and in the self-chat the agent would answer its own
// replayed replies, which loops. Anything predating the run is dropped.
func TestIsStale_DropsReplayedHistory(t *testing.T) {
	p := New()
	p.startedAt = time.Now()

	if !p.isStale(p.startedAt.Add(-time.Hour)) {
		t.Error("an hour-old message should be treated as replayed history")
	}
	if !p.isStale(p.startedAt.Add(-staleGrace - time.Minute)) {
		t.Error("a message past the grace window should be stale")
	}
}

// A brief restart must still pick up messages sent while the agent was down.
func TestIsStale_KeepsRecentMessagesWithinGrace(t *testing.T) {
	p := New()
	p.startedAt = time.Now()

	if p.isStale(p.startedAt.Add(-time.Minute)) {
		t.Error("a message from just before startup should still be answered")
	}
	if p.isStale(p.startedAt.Add(time.Second)) {
		t.Error("a message sent after startup is live traffic")
	}
}

// Before Start the clock is unset; nothing should be judged stale.
func TestIsStale_NoStartTimeAcceptsEverything(t *testing.T) {
	p := New()
	if p.isStale(time.Now().Add(-24 * time.Hour)) {
		t.Error("with no start time recorded, nothing is stale")
	}
}

// A zero timestamp carries no information — don't drop on it.
func TestIsStale_ZeroTimestampNotStale(t *testing.T) {
	p := New()
	p.startedAt = time.Now()
	if p.isStale(time.Time{}) {
		t.Error("a zero timestamp must not be treated as stale")
	}
}

func TestInit_SelfChatDefaultsOn(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !p.cfg.Admission.SelfChat {
		t.Error("self_chat should default to true — it is the personal-agent flow")
	}
	if p.cfg.Admission.AllowAnySender {
		t.Error("allowed_senders must default to owner-only, not open")
	}
}

func TestInit_AllowAnySenderOptIn(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(map[string]string{"allowed_senders": "anyone"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !p.cfg.Admission.AllowAnySender {
		t.Error("allowed_senders: anyone should disable the allowlist")
	}
}

func TestInit_SelfChatCanBeDisabled(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(map[string]string{"self_chat": "false"})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.cfg.Admission.SelfChat {
		t.Error("self_chat: false should disable it")
	}
}

// --- self-chat reply prefix ---

func TestApplyPrefix_PrependsMarker(t *testing.T) {
	if got := applyPrefix("⚒ Forge: ", "hello"); got != "⚒ Forge: hello" {
		t.Errorf("got %q", got)
	}
}

func TestApplyPrefix_EmptyPrefixIsNoop(t *testing.T) {
	if got := applyPrefix("", "hello"); got != "hello" {
		t.Errorf("got %q, want the chunk unchanged", got)
	}
}

// Inlining the marker before an opening fence makes WhatsApp render the fence
// literally instead of as a code block.
func TestApplyPrefix_FencedCodeGetsOwnLine(t *testing.T) {
	got := applyPrefix("⚒ Forge: ", "```go\nfmt.Println()\n```")
	if !strings.HasPrefix(got, "⚒ Forge: \n```go") {
		t.Errorf("marker should sit on its own line before a fence, got %q", got)
	}
}

func TestInit_SelfChatPrefixDefault(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.cfg.SelfChatPrefix != defaultSelfChatPrefix {
		t.Errorf("prefix = %q, want %q", p.cfg.SelfChatPrefix, defaultSelfChatPrefix)
	}
	if !strings.Contains(p.cfg.SelfChatPrefix, "Forge") {
		t.Errorf("default prefix should name Forge, got %q", p.cfg.SelfChatPrefix)
	}
}

func TestInit_SelfChatPrefixOverride(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(map[string]string{"self_chat_prefix": "🤖 bot: "})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.cfg.SelfChatPrefix != "🤖 bot: " {
		t.Errorf("prefix = %q, want the override", p.cfg.SelfChatPrefix)
	}
}

// An explicitly empty value means "no marker" and must not fall back to the
// default the way strOrDefault would.
func TestInit_SelfChatPrefixCanBeDisabled(t *testing.T) {
	p := New()
	if err := p.Init(cfgWith(map[string]string{"self_chat_prefix": ""})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.cfg.SelfChatPrefix != "" {
		t.Errorf("prefix = %q, want it disabled", p.cfg.SelfChatPrefix)
	}
}

// The marker exists because the self-chat has no sender distinction; a normal
// DM already shows who sent what, so marking there would just be noise.
func TestSelfChatPrefix_AppliesOnlyToSelfChat(t *testing.T) {
	cfg := admissionConfig{OwnJIDs: []string{testOwnJID}}

	if !cfg.isSelfChat(testOwnJID) {
		t.Error("the owner's own chat should be recognised as the self-chat")
	}
	for _, chat := range []string{"14155550100@s.whatsapp.net", "120363000000000000@g.us"} {
		if cfg.isSelfChat(chat) {
			t.Errorf("%q must not be treated as the self-chat", chat)
		}
	}
}
