package runtime

import (
	"os"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Environment variable names the admission middleware consumes (issue
// #201). Kept here as named constants so tests can reference them
// without string drift, and so the env surface is greppable across
// the codebase.
const (
	// EnvAdmissionURL points at the platform's admission endpoint.
	// Unset → admission middleware is off; pre-#201 behavior.
	EnvAdmissionURL = "FORGE_ADMISSION_URL"

	// EnvPlatformToken is the bearer token Forge sends as
	// Authorization on every admission call. Deliberately NOT named
	// FORGE_ADMISSION_TOKEN — the platform token is reusable for
	// future Forge → platform calls (audit forwarding, telemetry
	// upload, …) without inventing one env var per surface.
	EnvPlatformToken = "FORGE_PLATFORM_TOKEN"

	// EnvOrgID + EnvWorkspaceID are the existing tenancy env vars
	// from #157 — surfaced here so the admission outbound headers
	// (`Org-Id` / `Workspace-Id`) and the inbound tenancy stamps
	// (`X-Forge-Org-ID` / `X-Forge-Workspace-ID`) read from the same
	// source. Empty values produce no header on the wire.
	EnvOrgID       = "FORGE_ORG_ID"
	EnvWorkspaceID = "FORGE_WORKSPACE_ID"
)

// BuildAdmissionChecker resolves the admission configuration from
// env and returns either a PlatformAdmissionChecker (when both
// FORGE_ADMISSION_URL and FORGE_PLATFORM_TOKEN are set) or a
// NoopAdmissionChecker (when either is missing).
//
// Partial configuration — one of the pair set, the other missing —
// logs a startup warning so an operator who set only the URL by
// mistake sees the misconfiguration in the agent log rather than
// silently running without admission. This mirrors the
// guardrails-DB startup-warn pattern from issue #166.
//
// The agentID, orgID, and workspaceID are sourced from the agent's
// own configuration / env (#157) so the admission headers stay
// consistent with the inbound tenancy stamps on the same agent's
// audit events.
func BuildAdmissionChecker(agentID string, logger coreruntime.Logger) coreruntime.AdmissionChecker {
	admissionURL := os.Getenv(EnvAdmissionURL)
	platformToken := os.Getenv(EnvPlatformToken)

	switch {
	case admissionURL == "" && platformToken == "":
		// Both unset — the default deploy. Silent no-op.
		return coreruntime.NoopAdmissionChecker{}

	case admissionURL == "" && platformToken != "":
		// Operator set FORGE_PLATFORM_TOKEN but forgot the URL.
		// Token might be intended for some other Forge → platform
		// surface (future audit forwarding, etc.), so this is
		// expected to happen and not an error — just warn so the
		// admission-gating intent surfaces if that was the goal.
		if logger != nil {
			logger.Warn("admission: FORGE_PLATFORM_TOKEN set without FORGE_ADMISSION_URL; admission gating disabled", nil)
		}
		return coreruntime.NoopAdmissionChecker{}

	case admissionURL != "" && platformToken == "":
		// URL without token — almost certainly a misconfiguration
		// (the URL is admission-specific so the operator clearly
		// intended to engage it). Warn loudly with the missing env
		// name so it's a single-line fix in the deployment manifest.
		if logger != nil {
			logger.Warn("admission: FORGE_ADMISSION_URL set without FORGE_PLATFORM_TOKEN; admission gating disabled", map[string]any{
				"missing_env": EnvPlatformToken,
			})
		}
		return coreruntime.NoopAdmissionChecker{}
	}

	orgID := os.Getenv(EnvOrgID)
	workspaceID := os.Getenv(EnvWorkspaceID)

	if logger != nil {
		logger.Info("admission: engaged platform admission gating", map[string]any{
			"url":          admissionURL,
			"agent_id":     agentID,
			"org_id":       orgID,
			"workspace_id": workspaceID,
		})
	}

	return NewPlatformAdmissionChecker(
		admissionURL, agentID, orgID, workspaceID, platformToken, logger,
	)
}
