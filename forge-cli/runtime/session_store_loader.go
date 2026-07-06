package runtime

import (
	"os"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Environment variable names for the remote session store (issue #243).
// The remote backend deliberately reuses the admission tenancy/auth env
// (EnvPlatformToken / EnvOrgID / EnvWorkspaceID from admission_loader.go)
// so a single platform token and one set of tenancy stamps cover every
// Forge → platform surface.
const (
	// EnvSessionStore overrides memory.session_store: "file" | "remote".
	EnvSessionStore = "FORGE_SESSION_STORE"

	// EnvSessionStoreURL points at the platform session service. Required
	// when the backend resolves to "remote".
	EnvSessionStoreURL = "FORGE_SESSION_STORE_URL"

	// sessionStoreRemote is the opt-in backend value.
	sessionStoreRemote = "remote"
)

// buildRemoteSessionStore returns a remote-backed SessionStore when the
// agent is configured for it, or nil to signal "use the default file
// backend". Resolution: env (FORGE_SESSION_STORE) overrides config
// (memory.session_store); likewise FORGE_SESSION_STORE_URL overrides
// memory.session_store_url. Auth/tenancy come from the same env the
// admission client reads (FORGE_PLATFORM_TOKEN, FORGE_ORG_ID,
// FORGE_WORKSPACE_ID).
//
// A "remote" selection missing its URL or the platform token is a
// misconfiguration: rather than silently drop session persistence, we
// warn (single-line fix in the manifest) and fall back to the file
// backend by returning nil.
func buildRemoteSessionStore(agentID, cfgMode, cfgURL string, logger coreruntime.Logger) coreruntime.SessionStore {
	mode := cfgMode
	if v := os.Getenv(EnvSessionStore); v != "" {
		mode = v
	}
	if mode != sessionStoreRemote {
		return nil // file backend (default)
	}

	url := cfgURL
	if v := os.Getenv(EnvSessionStoreURL); v != "" {
		url = v
	}
	token := os.Getenv(EnvPlatformToken)

	switch {
	case url == "":
		if logger != nil {
			logger.Warn("session store: remote selected without a URL; using file backend", map[string]any{
				"missing_env": EnvSessionStoreURL,
			})
		}
		return nil
	case token == "":
		if logger != nil {
			logger.Warn("session store: remote selected without a platform token; using file backend", map[string]any{
				"missing_env": EnvPlatformToken,
			})
		}
		return nil
	}

	orgID := os.Getenv(EnvOrgID)
	workspaceID := os.Getenv(EnvWorkspaceID)

	if logger != nil {
		logger.Info("session store: engaged remote backend", map[string]any{
			"url":          url,
			"agent_id":     agentID,
			"org_id":       orgID,
			"workspace_id": workspaceID,
		})
	}

	return coreruntime.NewRemoteSessionStore(coreruntime.RemoteSessionStoreConfig{
		BaseURL:       url,
		AgentID:       agentID,
		OrgID:         orgID,
		WorkspaceID:   workspaceID,
		PlatformToken: token,
		Logger:        logger,
	})
}
