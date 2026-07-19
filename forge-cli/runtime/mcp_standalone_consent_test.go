package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/types"
)

// standaloneRunner builds a Runner configured with one standalone type: user
// MCP server (no platform block) and a public URL, with the consent loop wired.
func standaloneRunner(t *testing.T, tokenURL string) *Runner {
	t.Helper()
	r := &Runner{
		logger: nopLogger{},
		cfg: RunnerConfig{
			Config: &types.ForgeConfig{
				Server: types.ServerConfig{PublicURL: "https://agent.example"},
				MCP: types.MCPConfig{Servers: []types.MCPServer{{
					Name: "atl", URL: "https://mcp.atlassian.example/mcp",
					Auth: &types.MCPAuth{
						Type:         "user",
						ClientID:     "forge-client",
						AuthorizeURL: "https://idp.example/authorize",
						TokenURL:     tokenURL,
						Scopes:       []string{"read", "write"},
					},
				}}},
			},
		},
	}
	r.enableStandaloneConsent(http.DefaultClient)
	return r
}

// TestStandaloneConsent_LinkAndStart covers the front-half: building the consent
// link records a binding (session + PKCE verifier + authorize URL), and
// GET /mcp/oauth/start plants the session cookie and redirects to the IdP.
func TestStandaloneConsent_LinkAndStart(t *testing.T) {
	r := standaloneRunner(t, "https://idp.example/token")

	link, err := r.buildStandaloneConsentLink(context.Background(), "alice@corp.com", "atl")
	if err != nil {
		t.Fatalf("buildStandaloneConsentLink: %v", err)
	}
	if !strings.HasPrefix(link, "https://agent.example/mcp/oauth/start?state=") {
		t.Fatalf("link = %q, want a /mcp/oauth/start?state= URL", link)
	}
	u, _ := url.Parse(link)
	state := u.Query().Get("state")

	b, ok := r.stateBinder.Peek(state)
	if !ok {
		t.Fatal("no binding recorded for the issued state")
	}
	if b.subject != "alice@corp.com" || b.server != "atl" {
		t.Errorf("binding subject/server = %q/%q", b.subject, b.server)
	}
	if b.session == "" || b.verifier == "" {
		t.Error("binding must carry a non-empty session and PKCE verifier")
	}
	// The authorize URL must carry the redirect_uri, state, and PKCE challenge.
	az, _ := url.Parse(b.authorizeURL)
	if az.Query().Get("redirect_uri") != "https://agent.example/mcp/oauth/callback" {
		t.Errorf("redirect_uri = %q", az.Query().Get("redirect_uri"))
	}
	if az.Query().Get("state") != state || az.Query().Get("code_challenge") == "" {
		t.Error("authorize URL missing state or code_challenge")
	}

	// /start → 302 to the IdP + Set-Cookie forge_session = the bound session.
	rec := httptest.NewRecorder()
	makeMCPStartHandler(r.stateBinder)(rec, httptest.NewRequest("GET", "/mcp/oauth/start?state="+url.QueryEscape(state), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("/start → %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != b.authorizeURL {
		t.Errorf("/start Location = %q, want the authorize URL", loc)
	}
	var sessionCookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == forgeSessionCookie {
			sessionCookie = c.Value
		}
	}
	if sessionCookie != b.session {
		t.Errorf("forge_session cookie = %q, want the bound session %q", sessionCookie, b.session)
	}

	// A bogus state is rejected (no cookie, no redirect).
	rec2 := httptest.NewRecorder()
	makeMCPStartHandler(r.stateBinder)(rec2, httptest.NewRequest("GET", "/mcp/oauth/start?state=nope", nil))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("/start with bad state → %d, want 400", rec2.Code)
	}
}

// TestStandaloneConsent_CompleterExchangesAndStores proves the callback
// completer exchanges the code (with the bound verifier) and caches the token
// per-subject, so the standalone resolver then finds a grant.
func TestStandaloneConsent_CompleterExchangesAndStores(t *testing.T) {
	var gotForm url.Values
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		gotForm = req.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-alice", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer idp.Close()

	r := standaloneRunner(t, idp.URL)
	completer := r.makeStandaloneCompleter(idp.Client())

	if err := completer(context.Background(), "alice@corp.com", "atl", "authcode123", "verifier-xyz"); err != nil {
		t.Fatalf("completer: %v", err)
	}
	// The exchange must present the code, verifier, and matching redirect_uri.
	if gotForm.Get("code") != "authcode123" || gotForm.Get("code_verifier") != "verifier-xyz" {
		t.Errorf("token request code/verifier = %q/%q", gotForm.Get("code"), gotForm.Get("code_verifier"))
	}
	if gotForm.Get("redirect_uri") != "https://agent.example/mcp/oauth/callback" {
		t.Errorf("token request redirect_uri = %q", gotForm.Get("redirect_uri"))
	}
	// Token is now cached for the subject.
	tok, ok := r.standaloneSubjectStore.Get("alice@corp.com")
	if !ok || tok != "access-alice" {
		t.Fatalf("subject store Get = %q,%v, want access-alice", tok, ok)
	}
	// And a different subject still has nothing.
	if _, ok := r.standaloneSubjectStore.Get("eve@corp.com"); ok {
		t.Error("unrelated subject must not have a grant")
	}
}

// TestStandaloneConsent_DelivererWritesArtifact proves the standalone
// ConsentDeliverer publishes the login link on the task's auth-required artifact.
func TestStandaloneConsent_DelivererWritesArtifact(t *testing.T) {
	r := standaloneRunner(t, "https://idp.example/token")
	r.taskStore = a2a.NewTaskStore()
	r.taskStore.Put(&a2a.Task{ID: "task-1", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}})

	err := r.standaloneConsentDeliverer(context.Background(), "alice@corp.com", "atl", "task-1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("deliverer: %v", err)
	}

	task := r.taskStore.Get("task-1")
	if task.Status.State != a2a.TaskStateAuthRequired {
		t.Fatalf("task state = %q, want auth-required", task.Status.State)
	}
	if task.Status.Message == nil || len(task.Status.Message.Parts) < 2 {
		t.Fatal("auth-required message missing text + data parts")
	}
	text := task.Status.Message.Parts[0].Text
	if !strings.Contains(text, "/mcp/oauth/start?state=") {
		t.Errorf("artifact text lacks the consent link: %q", text)
	}
	data, _ := task.Status.Message.Parts[1].Data.(map[string]any)
	if data["authorize_url"] == "" || data["server"] != "atl" {
		t.Errorf("data part = %v, want authorize_url + server", data)
	}
}

// TestStandaloneConsent_NotWiredWhenManaged confirms a platform block disables
// the standalone wiring (managed mode owns delegation).
func TestStandaloneConsent_NotWiredWhenManaged(t *testing.T) {
	r := &Runner{
		logger: nopLogger{},
		cfg: RunnerConfig{Config: &types.ForgeConfig{
			Platform: &types.PlatformConfig{TokenEndpoint: "https://platform.example/token"},
			MCP: types.MCPConfig{Servers: []types.MCPServer{{
				Name: "atl", Auth: &types.MCPAuth{Type: "user", Ref: "mcp.atl"},
			}}},
		}},
	}
	r.enableStandaloneConsent(http.DefaultClient)
	if r.standaloneSubjectStore != nil || r.consentDeliverer != nil || r.callbackCompleter != nil {
		t.Error("managed mode must not wire the standalone consent loop")
	}
	// Sanity: the helper wouldn't create the in-memory store either.
	_ = mcp.NewInMemorySubjectTokenStore
}
