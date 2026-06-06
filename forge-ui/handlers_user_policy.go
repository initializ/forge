package forgeui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/security"
	"gopkg.in/yaml.v3"
)

// userPolicyResponse is the GET payload describing the user policy
// (~/.forge/policy.yaml) and the read-only state of the other two
// layers. The frontend renders the user policy as editable form
// fields; system and workspace layers are display-only (with a
// "denied by system / workspace policy" badge on channel chips that
// are blocked higher up).
//
// See issue #90 / FWS-6 (three-layer policy resolution).
type userPolicyResponse struct {
	// Path is the on-disk location of the user policy file (typically
	// ~/.forge/policy.yaml). Empty when the runtime can't determine a
	// home directory.
	Path string `json:"path"`
	// User is the parsed user policy. May be the zero value when no
	// user policy file exists yet.
	User security.PlatformPolicy `json:"user"`
	// System is the parsed system policy (read-only from the UI's
	// perspective). Surfaced so the frontend can render
	// system-denied channels as locked toggles.
	System security.PlatformPolicy `json:"system,omitempty"`
	// SystemPath is the on-disk location of the system policy.
	SystemPath string `json:"system_path,omitempty"`
	// Workspace is the parsed workspace policy (read-only — set by
	// the operator at deploy time via FORGE_PLATFORM_POLICY).
	Workspace security.PlatformPolicy `json:"workspace,omitempty"`
	// WorkspacePath is the on-disk location of the workspace policy.
	WorkspacePath string `json:"workspace_path,omitempty"`
}

// userPolicyUpdateRequest is the PUT body. Only the User field is
// writable — system + workspace are operator-controlled and not
// editable via the UI.
type userPolicyUpdateRequest struct {
	User security.PlatformPolicy `json:"user"`
}

// handleGetUserPolicy returns the current user policy plus a snapshot
// of the system + workspace layers so the frontend can render the
// effective state (a channel chip greyed out + locked = denied by a
// higher layer, not editable). All three reads are best-effort —
// missing files return zero policy without erroring (matches the
// runtime's optional-mount semantics from FWS-5).
func (s *UIServer) handleGetUserPolicy(w http.ResponseWriter, _ *http.Request) {
	resp := userPolicyResponse{
		Path:          security.UserPolicyPath(),
		SystemPath:    security.SystemPolicyPath(),
		WorkspacePath: security.WorkspacePolicyPath(),
	}
	if p, err := security.LoadPlatformPolicy(resp.Path); err == nil {
		resp.User = p
	}
	if p, err := security.LoadPlatformPolicy(resp.SystemPath); err == nil {
		resp.System = p
	}
	if resp.WorkspacePath != "" {
		if p, err := security.LoadPlatformPolicy(resp.WorkspacePath); err == nil {
			resp.Workspace = p
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePutUserPolicy writes the user policy YAML. The body is the
// full PlatformPolicy doc; the handler replaces the on-disk file
// rather than merging because the runtime treats the file as a
// complete document. Creates ~/.forge if missing.
//
// When the submitted policy is the zero value (every field empty),
// the file is removed from disk so a "no policy" state has no
// on-disk noise.
func (s *UIServer) handlePutUserPolicy(w http.ResponseWriter, r *http.Request) {
	var req userPolicyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := req.User.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "policy validation failed: "+err.Error())
		return
	}
	path := security.UserPolicyPath()
	if path == "" {
		writeError(w, http.StatusInternalServerError, "cannot determine user home directory")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "creating policy dir: "+err.Error())
		return
	}

	if req.User.IsZero() {
		// Empty policy → remove the file. Same on-disk shape as a
		// fresh install.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			writeError(w, http.StatusInternalServerError, "removing empty policy: "+err.Error())
			return
		}
	} else {
		data, err := yaml.Marshal(req.User)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "marshalling policy: "+err.Error())
			return
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			writeError(w, http.StatusInternalServerError, "writing policy: "+err.Error())
			return
		}
	}

	// Echo back the resulting state so the frontend can refresh
	// without a second GET.
	s.handleGetUserPolicy(w, r)
}
