package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/mcp"
	"github.com/initializ/forge/forge-core/types"
)

// This file is the STANDALONE (no-platform) delegated-consent front-half
// (#332). It turns a grantless type: user + grant: authorization_code MCP call
// into a browser OAuth flow that Forge itself drives:
//
//	park (ErrNoToken)  →  deliver a "Connect <server>" link on the A2A
//	auth-required artifact  →  GET /mcp/oauth/start sets the session cookie and
//	redirects to the IdP  →  GET /mcp/oauth/callback exchanges the code and
//	stores the token in the shared SubjectStore  →  the parked call resumes and
//	the standalone resolver (server.go) finds the grant.
//
// Managed mode (a platform block is present) uses none of this — the platform
// delivers the prompt, hosts the callback, and brokers the token.

// AuthorizeURLProvider supplies the consent link the deliverer presents to the
// user. Standalone builds it here (buildStandaloneConsentLink); a managed
// platform can supply its own pre-built URL via SetAuthorizeURLProvider (the
// seam #343's Slack delivery consumes so the same delivery code serves both
// modes). Returns the link to open, or an error the deliverer surfaces.
type AuthorizeURLProvider func(ctx context.Context, subject, server string) (string, error)

// SetAuthorizeURLProvider overrides how the consent link is built. Standalone
// wires its own by default; a managed platform sets one that returns its
// own authorize URL (its client_id/state/redirect_uri) so Forge never
// constructs a managed URL from local config. Must be called before Run().
func (r *Runner) SetAuthorizeURLProvider(fn AuthorizeURLProvider) {
	r.authorizeURLProvider = fn
}

// standaloneDelegatedServers returns the type: user MCP servers running in
// standalone mode (no platform block). Empty ⇒ nothing to wire.
func (r *Runner) standaloneDelegatedServers() []types.MCPServer {
	p := r.cfg.Config.Platform
	if p != nil && p.TokenEndpoint != "" {
		return nil // managed — the platform owns delegation
	}
	var out []types.MCPServer
	for _, s := range r.cfg.Config.MCP.Servers {
		if s.Auth != nil && s.Auth.Type == "user" {
			out = append(out, s)
		}
	}
	return out
}

// enableStandaloneConsent wires the standalone consent loop when the config has
// at least one standalone type: user server. It creates the shared
// SubjectStore (read by the resolver, written by the callback), and — unless an
// operator/platform already set them — installs the callback completer, the
// A2A-artifact deliverer, and the authorize-URL provider. Called before the MCP
// manager starts so the store is handed to it, and before the consent endpoints
// are registered. egressClient routes the token exchange through the allowlist.
func (r *Runner) enableStandaloneConsent(egressClient *http.Client) {
	if len(r.standaloneDelegatedServers()) == 0 {
		return
	}
	if r.standaloneSubjectStore == nil {
		r.standaloneSubjectStore = mcp.NewInMemorySubjectTokenStore()
	}
	if r.stateBinder == nil {
		r.stateBinder = newStateBinder(defaultStateTTL)
	}
	if r.authorizeURLProvider == nil {
		r.authorizeURLProvider = r.buildStandaloneConsentLink
	}
	if r.callbackCompleter == nil {
		r.callbackCompleter = r.makeStandaloneCompleter(egressClient)
	}
	if r.consentDeliverer == nil {
		r.consentDeliverer = r.standaloneConsentDeliverer
	}
}

// publicBaseURL is the agent's externally-reachable base URL (no trailing
// slash), used to build the OAuth redirect_uri. Precedence: server.public_url,
// then the AGENT_URL env var. Empty ⇒ the consent flow can't build a callback
// the IdP can redirect to, and delivery fails with a clear error.
func (r *Runner) publicBaseURL() string {
	base := r.cfg.Config.Server.PublicURL
	if base == "" {
		base = os.Getenv("AGENT_URL")
	}
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

// callbackRedirectURI is the redirect_uri baked into the authorize URL and
// replayed at the token exchange — they MUST match, so both derive from here.
func (r *Runner) callbackRedirectURI() (string, error) {
	base := r.publicBaseURL()
	if base == "" {
		return "", fmt.Errorf("standalone consent needs the agent's public URL: set server.public_url in forge.yaml or the AGENT_URL env var")
	}
	return base + "/mcp/oauth/callback", nil
}

// mcpServerSpec returns the configured spec for a server name.
func (r *Runner) mcpServerSpec(name string) (types.MCPServer, bool) {
	for _, s := range r.cfg.Config.MCP.Servers {
		if s.Name == name {
			return s, true
		}
	}
	return types.MCPServer{}, false
}

// buildStandaloneConsentLink is the standalone AuthorizeURLProvider. It mints a
// fresh PKCE pair + state + session, builds the IdP authorize URL, records the
// binding, and returns the /mcp/oauth/start link the user clicks. The session
// lives only in the binding + cookie (never a URL), so it isn't leaked to the
// IdP via Referer.
func (r *Runner) buildStandaloneConsentLink(_ context.Context, subject, server string) (string, error) {
	spec, ok := r.mcpServerSpec(server)
	if !ok || spec.Auth == nil {
		return "", fmt.Errorf("standalone consent: unknown or non-authed server %q", server)
	}
	redirectURI, err := r.callbackRedirectURI()
	if err != nil {
		return "", err
	}
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return "", err
	}
	state, err := oauth.GenerateState()
	if err != nil {
		return "", err
	}
	session, err := oauth.GenerateState()
	if err != nil {
		return "", err
	}
	authorizeURL, err := mcp.BuildAuthorizeURL(spec.Auth.AuthorizeURL, spec.Auth.ClientID, redirectURI, state, pkce.Challenge, spec.Auth.Scopes)
	if err != nil {
		return "", err
	}
	r.stateBinder.Bind(state, subject, server, session, pkce.Verifier, authorizeURL)
	return r.publicBaseURL() + "/mcp/oauth/start?state=" + url.QueryEscape(state), nil
}

// makeStandaloneCompleter returns the CallbackCompleter that exchanges the code
// (with the state-bound PKCE verifier) for a token and caches it per-subject.
// Only after this succeeds does the callback resume the parked call.
func (r *Runner) makeStandaloneCompleter(egressClient *http.Client) CallbackCompleter {
	client := egressClient
	if client == nil {
		client = http.DefaultClient
	}
	return func(ctx context.Context, subject, server, code, verifier string) error {
		spec, ok := r.mcpServerSpec(server)
		if !ok || spec.Auth == nil {
			return fmt.Errorf("standalone consent: unknown or non-authed server %q", server)
		}
		redirectURI, err := r.callbackRedirectURI()
		if err != nil {
			return err
		}
		tok, err := oauth.ExchangeCodeCtx(ctx, client, spec.Auth.TokenURL, spec.Auth.ClientID, code, redirectURI, verifier)
		if err != nil {
			return err
		}
		if tok.AccessToken == "" {
			return fmt.Errorf("standalone consent: token endpoint returned no access_token for server %q", server)
		}
		r.standaloneSubjectStore.Put(subject, tok.AccessToken, tokenTTL(tok))
		return nil
	}
}

// tokenTTL derives a cache lifetime from the token, defaulting conservatively
// when the IdP doesn't advertise one.
func tokenTTL(tok *oauth.Token) time.Duration {
	if !tok.ExpiresAt.IsZero() {
		if d := time.Until(tok.ExpiresAt); d > 0 {
			return d
		}
	}
	if tok.ExpiresIn > 0 {
		return time.Duration(tok.ExpiresIn) * time.Second
	}
	return 5 * time.Minute
}

// standaloneConsentDeliverer is the ConsentDeliverer that "delivers" the login
// link in-band: it builds the link and writes it onto the parked task's
// auth-required artifact so a UI/A2A client renders a clickable prompt. Channel
// (Slack) delivery is #343; this is the platform-free default.
func (r *Runner) standaloneConsentDeliverer(ctx context.Context, subject, server, taskID string, deadline time.Time) error {
	link, err := r.authorizeURLProvider(ctx, subject, server)
	if err != nil {
		return err
	}
	if r.taskStore == nil {
		return fmt.Errorf("standalone consent: no task store to publish the auth-required artifact")
	}
	r.SetStatus(taskID, a2a.TaskStatus{
		State: a2a.TaskStateAuthRequired,
		Message: &a2a.Message{
			Role: a2a.MessageRoleAgent,
			Parts: []a2a.Part{
				a2a.NewTextPart(fmt.Sprintf("Authorization required: connect %s (as %s). Open this link to continue: %s", server, subject, link)),
				a2a.NewDataPart(map[string]any{
					"type":          "mcp_auth_required",
					"server":        server,
					"subject":       subject,
					"authorize_url": link,
					"expires_at":    deadline.UTC().Format(time.RFC3339),
				}),
			},
		},
	})
	return nil
}
