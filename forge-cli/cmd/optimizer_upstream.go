package cmd

import (
	"os"

	"github.com/initializ/forge/forge-core/settings"
)

// Upstream resolution for the optimizer proxy, shared by the foreground serve
// path (buildOptimizerSetup) and the detached daemon (resolveChildUpstream) so
// both agree. Precedence, highest first:
//
//  1. MANAGED settings  — optimizer.upstream in forge's managed-settings.json at
//     the fixed OS system path (admin/MDM-owned; see forge-core/settings). It is
//     ENFORCED: it overrides even --upstream so an org can pin the gateway.
//  2. --upstream flag
//  3. $FORGE_OPTIMIZER_UPSTREAM
//  4. USER settings     — optimizer.upstream in ~/.forge/settings.json (and the
//     trusted project-local / CLI layers), via the shared settings loader.
//  5. chaining          — caller-supplied (ANTHROPIC_BASE_URL for the serve path,
//     the Claude Code settings gateway for the daemon).
//
// SECURITY: the upstream is an endpoint redirect for Claude Code's traffic (and
// its Authorization header), so — exactly like a model gateway's base_url — it
// MUST NOT be honorable from the checked-in project layer (.forge/settings.json
// ships inside a cloned repo). We resolve over TrustedGatewayLayers, which drops
// that layer. Returns "" when nothing is configured.
func resolveOptimizerUpstream(flag, chaining string) string {
	layers, err := settings.LoadAllLayers(settings.LoadOptions{})
	if err != nil {
		layers = nil // settings are best-effort; never block the proxy on a bad file
	}
	trusted := settings.TrustedGatewayLayers(layers)

	managed := ""
	if ml := settings.ManagedLayer(trusted); ml != nil {
		managed = ml.Settings.Optimizer.Upstream
	}
	user := settings.Resolve(trusted).Optimizer.Upstream

	return pickUpstream(managed, flag, os.Getenv("FORGE_OPTIMIZER_UPSTREAM"), user, chaining)
}

// pickUpstream is the pure precedence function (no I/O), for testability.
func pickUpstream(managed, flag, env, user, chaining string) string {
	switch {
	case managed != "":
		return managed
	case flag != "":
		return flag
	case env != "":
		return env
	case user != "":
		return user
	default:
		return chaining
	}
}
