package runtime

import (
	"context"
	"net/http"

	"github.com/initializ/forge/forge-core/mcp"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

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
