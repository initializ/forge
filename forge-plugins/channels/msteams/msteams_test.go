package msteams

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-plugins/channels/markdown"
)

// --- dedup ---

func TestDedup_SeenAndMark(t *testing.T) {
	d := newDedup(10)
	if d.seen("a") {
		t.Error("fresh dedup should not see a")
	}
	d.mark("a")
	if !d.seen("a") {
		t.Error("after mark, seen should be true")
	}
}

func TestDedup_RingEviction(t *testing.T) {
	d := newDedup(3)
	d.mark("a")
	d.mark("b")
	d.mark("c")
	if d.size() != 3 {
		t.Errorf("size = %d, want 3", d.size())
	}
	d.mark("d") // evicts "a"
	if d.seen("a") {
		t.Error("oldest entry (a) should have been evicted")
	}
	if !d.seen("d") {
		t.Error("newest entry (d) should be present")
	}
	if d.size() != 3 {
		t.Errorf("size after eviction = %d, want 3", d.size())
	}
}

func TestDedup_DuplicateMarkNoEvict(t *testing.T) {
	d := newDedup(2)
	d.mark("a")
	d.mark("b")
	d.mark("a") // duplicate — should not evict b
	if !d.seen("b") {
		t.Error("b should still be present after duplicate mark of a")
	}
}

// --- cursor ---

func TestCursor_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channels", "msteams-cursor.json")

	c := newCursor(path)
	if got := c.load(); got != "" {
		t.Errorf("fresh cursor should load empty, got %q", got)
	}

	if err := c.save("https://example/delta-1"); err != nil {
		t.Fatalf("save: %v", err)
	}

	// New cursor instance — load from disk.
	c2 := newCursor(path)
	if got := c2.load(); got != "https://example/delta-1" {
		t.Errorf("loaded cursor = %q, want delta-1", got)
	}

	// Mode 0700 on the directory.
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("dir perm = %o, want 0700", info.Mode().Perm())
	}
}

func TestCursor_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "msteams-cursor.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	c := newCursor(path)
	if got := c.load(); got != "" {
		t.Errorf("corrupt file should load empty, got %q", got)
	}
}

// --- admission ---

func newUserMsg(userID, body string) *ChatMessage {
	m := &ChatMessage{ID: "msg-1"}
	m.From = &struct {
		User *struct {
			ID               string `json:"id"`
			DisplayName      string `json:"displayName"`
			UserIdentityType string `json:"userIdentityType,omitempty"`
			TenantID         string `json:"tenantId,omitempty"`
		} `json:"user,omitempty"`
		Application *struct {
			ID                  string `json:"id"`
			DisplayName         string `json:"displayName"`
			ApplicationIdentity string `json:"applicationIdentityType,omitempty"`
		} `json:"application,omitempty"`
	}{
		User: &struct {
			ID               string `json:"id"`
			DisplayName      string `json:"displayName"`
			UserIdentityType string `json:"userIdentityType,omitempty"`
			TenantID         string `json:"tenantId,omitempty"`
		}{ID: userID, DisplayName: "Author"},
	}
	m.Body.ContentType = "html"
	m.Body.Content = body
	return m
}

func newBotMsg(botID, body string) *ChatMessage {
	m := &ChatMessage{ID: "msg-bot"}
	m.From = &struct {
		User *struct {
			ID               string `json:"id"`
			DisplayName      string `json:"displayName"`
			UserIdentityType string `json:"userIdentityType,omitempty"`
			TenantID         string `json:"tenantId,omitempty"`
		} `json:"user,omitempty"`
		Application *struct {
			ID                  string `json:"id"`
			DisplayName         string `json:"displayName"`
			ApplicationIdentity string `json:"applicationIdentityType,omitempty"`
		} `json:"application,omitempty"`
	}{
		Application: &struct {
			ID                  string `json:"id"`
			DisplayName         string `json:"displayName"`
			ApplicationIdentity string `json:"applicationIdentityType,omitempty"`
		}{ID: botID, DisplayName: "TestBot"},
	}
	m.Body.ContentType = "html"
	m.Body.Content = body
	return m
}

func TestAdmit_SelfAuthoredUserMessageAdmitted(t *testing.T) {
	// In delegated mode the agent shares the user's Graph identity, so a
	// message authored by ownUserID is NOT inherently a loop — it might be
	// the user typing from a Teams client. Admit it through admission and
	// rely on the dedup ring (populated by SendResponse with outbound
	// message IDs) to catch genuine agent-authored loops upstream of admit.
	m := newUserMsg("own-id", "hi agent")
	res := admit(m, "own-id", nil, AdmitDM, "oneOnOne")
	if !res.admit {
		t.Errorf("delegated self-authored DM should be admitted (dedup handles loops); got reason=%q", res.reason)
	}
}

func TestAdmit_DM(t *testing.T) {
	m := newUserMsg("alice", "hello")
	res := admit(m, "bot-id", nil, AdmitDM, "oneOnOne")
	if !res.admit {
		t.Errorf("DM message under admit=dm should be admitted, got %q", res.reason)
	}
}

func TestAdmit_DMRejectsGroup(t *testing.T) {
	m := newUserMsg("alice", "hello")
	res := admit(m, "bot-id", nil, AdmitDM, "group")
	if res.admit {
		t.Error("group message under admit=dm should be dropped")
	}
}

func TestAdmit_MentionOnly(t *testing.T) {
	mentions := []markdown.TeamsMention{
		{ID: 0, Text: "Bot", Mentioned: struct {
			User struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"user"`
		}{User: struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}{ID: "bot-id", DisplayName: "Bot"}}},
	}
	m := newUserMsg("alice", `<at id="0">Bot</at> hi`)
	m.Mentions = mentions
	res := admit(m, "bot-id", nil, AdmitMention, "group")
	if !res.admit {
		t.Errorf("mention in group under admit=mention should be admitted, got %q", res.reason)
	}
}

func TestAdmit_MentionOnly_NoMention_Dropped(t *testing.T) {
	m := newUserMsg("alice", "hello channel")
	res := admit(m, "bot-id", nil, AdmitMention, "group")
	if res.admit {
		t.Error("non-mention group message under admit=mention should be dropped")
	}
}

func TestAdmit_MentionOrDM_DM(t *testing.T) {
	m := newUserMsg("alice", "hi")
	res := admit(m, "bot-id", nil, AdmitMentionOrDM, "oneOnOne")
	if !res.admit {
		t.Errorf("DM under mention_or_dm should be admitted: %q", res.reason)
	}
}

func TestAdmit_BotNotInAllowlist_Dropped(t *testing.T) {
	m := newBotMsg("rando-bot", "automated notice")
	res := admit(m, "bot-id", map[string]bool{}, AdmitDM, "oneOnOne")
	if res.admit {
		t.Error("bot not in allow_bot_ids should be dropped even on DM")
	}
}

func TestAdmit_BotInAllowlist_Admitted(t *testing.T) {
	m := newBotMsg("ci-bot", "build green")
	res := admit(m, "bot-id", map[string]bool{"ci-bot": true}, AdmitDM, "oneOnOne")
	if !res.admit {
		t.Errorf("allowlisted bot on DM should be admitted: %q", res.reason)
	}
}

// --- stripBotMention ---

func TestStripBotMention(t *testing.T) {
	cases := []struct{ in, name, want string }{
		{"@Bot please help", "Bot", "please help"},
		{"  @Bot: do the thing", "Bot", "do the thing"},
		{"@bot, why?", "Bot", "why?"}, // case-insensitive prefix match
		{"hi @Bot", "Bot", "hi @Bot"}, // not at start — left alone
		{"just text", "Bot", "just text"},
		{"@Other go away", "Bot", "@Other go away"},
	}
	for _, c := range cases {
		got := stripBotMention(c.in, c.name)
		if got != c.want {
			t.Errorf("stripBotMention(%q, %q) = %q, want %q", c.in, c.name, got, c.want)
		}
	}
}

// --- parseAllowBotIDs ---

func TestParseAllowBotIDs(t *testing.T) {
	cases := []struct {
		in     string
		expect []string
	}{
		{"", nil},
		{"bot-a", []string{"bot-a"}},
		{"a, b , c", []string{"a", "b", "c"}},
		{"x y z", []string{"x", "y", "z"}},
	}
	for _, c := range cases {
		got := parseAllowBotIDs(c.in)
		for _, want := range c.expect {
			if !got[want] {
				t.Errorf("parseAllowBotIDs(%q) missing %q", c.in, want)
			}
		}
		if len(got) != len(c.expect) {
			t.Errorf("parseAllowBotIDs(%q) size = %d, want %d", c.in, len(got), len(c.expect))
		}
	}
}

// --- graph client with httptest.Server ---

// newFakeAuthManager returns an authManager pre-seeded with a hard-coded
// access token and a far-future expiry, so graphClient tests can bypass the
// real token endpoint.
func newFakeAuthManager(token string) *authManager {
	// authManager.Token() checks expiresAt > 60s in the future, so we pre-seed
	// with a far-future expiry.
	a := &authManager{cached: token, expiresAt: time.Now().Add(time.Hour)}
	return a
}

func TestGraphClient_Me(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me" {
			t.Errorf("path = %q, want /me", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("auth header = %q, want Bearer test-token", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(MeResponse{ID: "user-1", DisplayName: "Test User", UserPrincipalName: "test@example.com"})
	}))
	defer srv.Close()

	g := newGraphClient(srv.URL, srv.Client(), newFakeAuthManager("test-token"))
	me, err := g.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me.ID != "user-1" || me.DisplayName != "Test User" {
		t.Errorf("me = %+v", me)
	}
}

func TestGraphClient_FetchDeltaPage_NextLink(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(DeltaPage{
			Messages: []ChatMessage{{ID: "m1"}, {ID: "m2"}},
			NextLink: "https://other-host/next-page",
		})
	}))
	defer srv.Close()

	g := newGraphClient(srv.URL, srv.Client(), newFakeAuthManager("t"))
	page, err := g.FetchDeltaPage(context.Background(), srv.URL+"/users/u/chats/getAllMessages/delta")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(page.Messages) != 2 || page.NextLink == "" {
		t.Errorf("page = %+v", page)
	}
}

func TestGraphClient_401Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	g := newGraphClient(srv.URL, srv.Client(), newFakeAuthManager("t"))
	_, err := g.Me(context.Background())
	if err != errUnauthorized {
		t.Errorf("err = %v, want errUnauthorized", err)
	}
}

func TestGraphClient_403Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	g := newGraphClient(srv.URL, srv.Client(), newFakeAuthManager("t"))
	_, err := g.Me(context.Background())
	if err != errForbidden {
		t.Errorf("err = %v, want errForbidden", err)
	}
}

func TestGraphClient_410CursorExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	g := newGraphClient(srv.URL, srv.Client(), newFakeAuthManager("t"))
	_, err := g.FetchDeltaPage(context.Background(), srv.URL+"/users/u/chats/getAllMessages/delta")
	if err != errCursorExpired {
		t.Errorf("err = %v, want errCursorExpired", err)
	}
}

func TestGraphClient_429RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	g := newGraphClient(srv.URL, srv.Client(), newFakeAuthManager("t"))
	_, err := g.FetchDeltaPage(context.Background(), srv.URL+"/users/u/chats/getAllMessages/delta")
	rl, ok := err.(*rateLimitedError)
	if !ok {
		t.Fatalf("err type = %T, want *rateLimitedError", err)
	}
	if rl.RetryAfter != 15*time.Second {
		t.Errorf("retry = %s, want 15s", rl.RetryAfter)
	}
	if !errIsRateLimited(err) {
		t.Error("errIsRateLimited should detect rate-limit error")
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 10 * time.Second},
		{"5", 10 * time.Second}, // below floor → clamped to 10s
		{"30", 30 * time.Second},
		{"500", 300 * time.Second}, // above ceiling → clamped to 300s
		{"garbage", 10 * time.Second},
	}
	for _, c := range cases {
		got := parseRetryAfter(c.in)
		if got != c.want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestGraphClient_PostChatMessage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"msg-out-1"}`))
	}))
	defer srv.Close()
	g := newGraphClient(srv.URL, srv.Client(), newFakeAuthManager("t"))
	id, err := g.PostChatMessage(context.Background(), "chat-1", "<strong>hi</strong>")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if id != "msg-out-1" {
		t.Errorf("PostChatMessage id = %q, want msg-out-1", id)
	}
	body, ok := gotBody["body"].(map[string]any)
	if !ok {
		t.Fatalf("body field missing: %+v", gotBody)
	}
	if body["contentType"] != "html" {
		t.Errorf("contentType = %v, want html", body["contentType"])
	}
}

// --- auth manager ---

func TestAuthManager_DelegatedRefresh(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		_, _ = w.Write([]byte(`{"access_token":"acc-1","token_type":"Bearer","expires_in":3600,"refresh_token":"new-refresh"}`))
	}))
	defer srv.Close()

	var rotated string
	a := newAuthManager(authConfig{
		TenantID:              "tenant",
		ClientID:              "client",
		ClientSecret:          "secret",
		RefreshToken:          "old-refresh",
		Flow:                  FlowDelegated,
		LoginBaseURL:          srv.URL,
		OnRefreshTokenRotated: func(s string) { rotated = s },
	}, srv.Client())

	tok, err := a.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "acc-1" {
		t.Errorf("token = %q, want acc-1", tok)
	}
	if rotated != "new-refresh" {
		t.Errorf("rotated = %q, want new-refresh", rotated)
	}
	// Second call within expiry should use cache, no new HTTP hit.
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("expected token endpoint hit exactly once, got %d", hits)
	}
}

func TestAuthManager_ClientCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		_, _ = w.Write([]byte(`{"access_token":"app-1","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	a := newAuthManager(authConfig{
		TenantID:     "tenant",
		ClientID:     "client",
		ClientSecret: "secret",
		Flow:         FlowClientCredentials,
		LoginBaseURL: srv.URL,
	}, srv.Client())

	tok, err := a.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "app-1" {
		t.Errorf("token = %q, want app-1", tok)
	}
}

func TestAuthManager_TokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token expired"}`))
	}))
	defer srv.Close()
	a := newAuthManager(authConfig{
		TenantID:     "tenant",
		ClientID:     "client",
		RefreshToken: "expired",
		Flow:         FlowDelegated,
		LoginBaseURL: srv.URL,
	}, srv.Client())
	_, err := a.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refresh token expired") {
		t.Errorf("err = %v, want refresh token expired", err)
	}
}

// --- plugin Init validation ---

func TestPlugin_Init_RequiresTenant(t *testing.T) {
	p := New()
	err := p.Init(mkConfig(map[string]string{
		"client_id":     "c",
		"refresh_token": "r",
	}))
	if err == nil || !strings.Contains(err.Error(), "tenant_id") {
		t.Errorf("err = %v, want tenant_id required", err)
	}
}

func TestPlugin_Init_DelegatedNeedsRefreshToken(t *testing.T) {
	p := New()
	err := p.Init(mkConfig(map[string]string{
		"tenant_id": "t",
		"client_id": "c",
		"auth_flow": "delegated",
	}))
	if err == nil || !strings.Contains(err.Error(), "refresh_token") {
		t.Errorf("err = %v, want refresh_token required", err)
	}
}

func TestPlugin_Init_ClientCredentialsNeedsUserID(t *testing.T) {
	p := New()
	err := p.Init(mkConfig(map[string]string{
		"tenant_id":     "t",
		"client_id":     "c",
		"client_secret": "s",
		"auth_flow":     "client_credentials",
	}))
	if err == nil || !strings.Contains(err.Error(), "user_id") {
		t.Errorf("err = %v, want user_id required", err)
	}
}

func TestPlugin_Init_PollIntervalClamp(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 5 * time.Second},
		{"1", 3 * time.Second}, // below floor
		{"10", 10 * time.Second},
		{"999", 60 * time.Second}, // above ceiling
	}
	for _, c := range cases {
		p := New()
		err := p.Init(mkConfig(map[string]string{
			"tenant_id":             "t",
			"client_id":             "c",
			"refresh_token":         "r",
			"poll_interval_seconds": c.in,
		}))
		if err != nil {
			t.Fatalf("Init(%q): %v", c.in, err)
		}
		if p.cfg.PollInterval != c.want {
			t.Errorf("Init(%q): poll = %s, want %s", c.in, p.cfg.PollInterval, c.want)
		}
	}
}

// mkConfig builds a ChannelConfig whose Settings map contains the given
// already-resolved values (no env-var indirection). The plugin's Init uses
// channels.ResolveEnvVars which strips _env suffixes — we pass raw keys so
// the values flow through unchanged.
func mkConfig(settings map[string]string) corechannelsConfig {
	cfg := corechannelsConfig{
		Adapter:  "msteams",
		Settings: settings,
	}
	return cfg
}

// Local alias so tests don't need to import the full channels package path.
type corechannelsConfig = struct {
	Adapter     string            `yaml:"adapter"`
	WebhookPort int               `yaml:"webhook_port,omitempty"`
	WebhookPath string            `yaml:"webhook_path,omitempty"`
	Settings    map[string]string `yaml:"settings,omitempty"`
}
