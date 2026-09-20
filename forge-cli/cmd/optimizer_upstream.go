package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Upstream resolution for the optimizer proxy, shared by the foreground serve
// path (buildOptimizerSetup) and the detached daemon (resolveChildUpstream) so
// both agree. Precedence, highest first:
//
//	1. MANAGED settings  — forge managed-settings.json at an OS system path
//	   (admin/MDM-owned). ENFORCED: overrides even --upstream so an org can pin
//	   the gateway, mirroring how Claude Code managed settings outrank the rest.
//	2. --upstream flag
//	3. $FORGE_OPTIMIZER_UPSTREAM
//	4. USER settings     — ~/.forge/settings.json
//	5. chaining          — caller-supplied (ANTHROPIC_BASE_URL for the serve path,
//	   the Claude Code settings gateway for the daemon)
//
// Returns "" when nothing is configured, letting the caller apply its default.

// forgeSettings is the subset of forge's settings.json this code reads. The file
// is JSON so it composes with other forge settings we may add later.
type forgeSettings struct {
	Optimizer struct {
		Upstream string `json:"upstream"`
	} `json:"optimizer"`
}

// userSettingsPath is forge's per-user settings file.
func userSettingsPath() string { return forgeFile("settings.json") }

// managedSettingsPath is forge's system-level, admin/MDM-owned settings file —
// per-OS, not user-writable, so it can enforce org policy. Intentionally NOT
// overridable by an env var (that would let a user defeat enforcement).
func managedSettingsPath() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/forge/managed-settings.json"
	case "windows":
		pd := os.Getenv("PROGRAMDATA")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "forge", "managed-settings.json")
	default: // linux and other unixes
		return "/etc/forge/managed-settings.json"
	}
}

// settingsUpstream reads optimizer.upstream from a forge settings.json file.
// Missing/unreadable/unparseable → "" (never fatal).
func settingsUpstream(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s forgeSettings
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.Optimizer.Upstream
}

// resolveOptimizerUpstream applies the precedence above. `chaining` is the
// path-specific fallback the caller supplies (see the two call sites).
func resolveOptimizerUpstream(flag, chaining string) string {
	return pickUpstream(
		settingsUpstream(managedSettingsPath()),
		flag,
		os.Getenv("FORGE_OPTIMIZER_UPSTREAM"),
		settingsUpstream(userSettingsPath()),
		chaining,
	)
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
