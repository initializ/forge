package cmd

import (
	"fmt"
	"net/url"
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
func resolveOptimizerUpstream(flag, chaining, listen string) string {
	// A chaining source (ANTHROPIC_BASE_URL / the Claude Code settings gateway)
	// legitimately equals our OWN address when it already points at the optimizer
	// (e.g. a repeat `start`). Treat that as "no chaining" so we fall back instead
	// of looping. An EXPLICIT self-reference (flag/env/settings) is a
	// misconfiguration and is rejected by validateUpstream, not silently dropped.
	if listen != "" && upstreamHost(chaining) == listen {
		chaining = ""
	}

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

// upstreamHost returns the host:port of a URL, or "" if it can't be parsed. Used
// only to compare against the listen address (self-loop detection).
func upstreamHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// validateUpstream rejects a malformed or self-referencing resolved upstream with
// a clear error, so a bad value (typo, non-URL, or a flag/settings value pointing
// at the optimizer itself) fails at `start` instead of at request time. Empty is
// OK — the caller applies its own default.
func validateUpstream(upstream, listen string) error {
	if upstream == "" {
		return nil
	}
	u, err := url.Parse(upstream)
	if err != nil {
		return fmt.Errorf("invalid optimizer upstream %q: %w", upstream, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("optimizer upstream %q must be an http(s) URL", upstream)
	}
	if u.Host == "" {
		return fmt.Errorf("optimizer upstream %q must include a host", upstream)
	}
	if listen != "" && u.Host == listen {
		return fmt.Errorf("optimizer upstream %q points at the optimizer's own listen address (self-loop)", upstream)
	}
	return nil
}
