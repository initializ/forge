package cmd

import (
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/types"
)

// Upstream resolution for the optimizer proxy, shared by the foreground serve
// path (buildOptimizerSetup) and the detached daemon (resolveChildUpstream) so
// both agree. Precedence, highest first:
//
//	1. MANAGED (enforced)  — $FORGE_MANAGED_OPTIMIZER_UPSTREAM, injected by the
//	   initializ platform (same convention as FORGE_ORG_ID). Overrides everything,
//	   including a developer's --upstream, so an org can pin the gateway.
//	2. --upstream flag
//	3. $FORGE_OPTIMIZER_UPSTREAM
//	4. USER config       — optimizer.upstream in forge.yaml (project ./forge.yaml,
//	   ./forge.yml, or the user-global ~/.forge/forge.yaml)
//	5. chaining          — caller-supplied (ANTHROPIC_BASE_URL for the serve path,
//	   the Claude Code settings gateway for the daemon)
//
// Returns "" when nothing is configured, letting the caller apply its default.

const forgeManagedUpstreamEnv = "FORGE_MANAGED_OPTIMIZER_UPSTREAM"

// resolveOptimizerUpstream applies the precedence above. `chaining` is the
// path-specific fallback the caller supplies (see the two call sites).
func resolveOptimizerUpstream(flag, chaining string) string {
	return pickUpstream(
		os.Getenv(forgeManagedUpstreamEnv),
		flag,
		os.Getenv("FORGE_OPTIMIZER_UPSTREAM"),
		forgeUserUpstream(),
		chaining,
	)
}

// pickUpstream is the pure precedence function (no I/O), for testability.
func pickUpstream(managed, flag, env, userCfg, chaining string) string {
	switch {
	case managed != "":
		return managed
	case flag != "":
		return flag
	case env != "":
		return env
	case userCfg != "":
		return userCfg
	default:
		return chaining
	}
}

// forgeUserUpstream returns optimizer.upstream from the first forge.yaml found
// among the project dir and the user-global ~/.forge, using the lenient
// extractor (a minimal ~/.forge/forge.yaml with only an optimizer block is
// valid). "" if none configures it.
func forgeUserUpstream() string {
	for _, p := range []string{
		"forge.yaml",
		"forge.yml",
		filepath.Join(forgeHome(), "forge.yaml"),
		filepath.Join(forgeHome(), "forge.yml"),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if up := types.OptimizerUpstreamFromYAML(b); up != "" {
			return up
		}
	}
	return ""
}
