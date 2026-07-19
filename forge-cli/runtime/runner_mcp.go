package runtime

import (
	"context"
	"net/http"
	"net/url"
	"os"

	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/mcp"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// oauthServerNames returns the names of the oauth-typed MCP servers.
func oauthServerNames(cfg types.MCPConfig) []string {
	var names []string
	for _, s := range cfg.Servers {
		if s.Auth != nil && s.Auth.Type == "oauth" {
			names = append(names, s.Name)
		}
	}
	return names
}

// mcpRegisteredOAuthHosts applies the MCP token-store override, then
// returns the authorize/token/registration hosts persisted at login
// time by OAuth discovery (#316) — merged into the egress allowlist so
// a discovered authorization-server host (absent from forge.yaml) is
// reachable at runtime.
func mcpRegisteredOAuthHosts(cfg types.MCPConfig) []string {
	if sp := mcpTokenStorePath(cfg.TokenStorePath); sp != "" {
		oauth.SetCredentialsDir(sp)
	}
	return mcp.RegisteredOAuthHosts(oauthServerNames(cfg))
}

// mcpTokenStorePath returns the effective OAuth credentials
// directory for MCP. Precedence (highest first):
//
//  1. forge.yaml mcp.token_store_path
//  2. env MCP_TOKEN_STORE_PATH
//  3. "" — caller falls back to oauth.DefaultCredentialsDir's
//     home-based default.
//
// Review B11 — TokenStorePath was previously parsed from YAML but
// never read.
func mcpTokenStorePath(fromYAML string) string {
	if fromYAML != "" {
		return fromYAML
	}
	return os.Getenv("MCP_TOKEN_STORE_PATH")
}

// startMCPManager constructs and starts the mcp.Manager when forge.yaml
// declares mcp.servers. Returns (nil, nil) when no MCP block is
// configured — that's the common case for agents that don't use MCP.
//
// On a Required=true server failure the Manager returns an error
// that this method propagates up; the caller (Runner.Run) converts
// that to a non-zero exit so Kubernetes observes a CrashLoopBackOff.
//
// The egressClient argument MUST be the egress-controlled
// *http.Client built earlier in Runner.Run — every MCP HTTP call
// rides the same allowlist as the rest of the agent.
func (r *Runner) startMCPManager(
	ctx context.Context,
	egressClient *http.Client,
	auditLogger *coreruntime.AuditLogger,
) (*mcp.Manager, error) {
	if len(r.cfg.Config.MCP.Servers) == 0 {
		return nil, nil
	}
	if egressClient == nil {
		egressClient = http.DefaultClient
	}

	// Wire the optional token-store-path override (review B11). YAML
	// field wins over env var; both override the ~/.forge/credentials
	// default. Empty here means "use default" — pass empty so any
	// previous override is cleared at the start of this Run.
	if storePath := mcpTokenStorePath(r.cfg.Config.MCP.TokenStorePath); storePath != "" {
		oauth.SetCredentialsDir(storePath)
	}

	// Build the shared OAuthFlow if any server uses oauth. Wiring the
	// audit callback here keeps mcp_token_refresh events flowing into
	// the same NDJSON stream as the rest of the MCP audit set.
	needsOAuth := false
	for _, s := range r.cfg.Config.MCP.Servers {
		if s.Auth != nil && s.Auth.Type == "oauth" {
			needsOAuth = true
			break
		}
	}
	var flow *mcp.OAuthFlow
	if needsOAuth {
		flow = mcp.NewOAuthFlow()
		// Route /token calls through the egress-controlled client so
		// token endpoints (auto-merged into the allowlist by
		// security.MCPDomains) ride the same enforcer as MCP traffic
		// — and a hung IdP can't bypass it (review B2).
		flow.HTTPClient = egressClient
		flow.AuditFn = func(server string, ok bool, reason string) {
			auditLogger.Emit(coreruntime.AuditEvent{
				Event: coreruntime.EventMCPTokenRefresh,
				Fields: map[string]any{
					"server": server,
					"ok":     ok,
					"reason": reason,
				},
			})
		}
	}

	mgr, err := mcp.NewManager(r.cfg.Config.MCP, mcp.ManagerDeps{
		HTTPClient: egressClient,
		Logger:     r.logger,
		Audit:      auditLogger,
		OAuth:      flow,
		Platform:   r.cfg.Config.Platform,
		// #332 — the standalone type: user resolver reads this shared store;
		// the consent callback writes to it. nil unless enableStandaloneConsent
		// created it (i.e. a standalone type: user server is configured).
		SubjectStore: r.standaloneSubjectStore,
	})
	if err != nil {
		return nil, err
	}
	if err := mgr.Start(ctx); err != nil {
		// Stop any servers that did come up before returning so the
		// caller doesn't leak goroutines.
		_ = mgr.Stop()
		return nil, err
	}
	r.logger.Info("mcp manager started", map[string]any{
		"servers":    len(r.cfg.Config.MCP.Servers),
		"tools":      len(mgr.Tools()),
		"oauth_used": needsOAuth,
	})
	return mgr, nil
}

// platformResolverHost returns the platform token-resolver host for the
// egress allowlist (auth.type=platform servers fetch tokens from it).
// Empty when no platform block is configured.
func platformResolverHost(p *types.PlatformConfig) []string {
	if p == nil || p.TokenEndpoint == "" {
		return nil
	}
	u, err := url.Parse(os.Expand(p.TokenEndpoint, os.Getenv))
	if err != nil || u.Hostname() == "" {
		return nil
	}
	return []string{u.Hostname()}
}
