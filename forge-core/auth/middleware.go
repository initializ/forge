package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Config controls bearer-token authentication for the A2A server.
type Config struct {
	// Enabled controls whether authentication is enforced.
	Enabled bool

	// Token is the expected bearer token value (local validation).
	Token string

	// AuthURL is an external auth provider endpoint. When set, the middleware
	// forwards the bearer token to this URL via POST for validation instead
	// of comparing against the local Token value.
	AuthURL string

	// AuthOrgID is the default org_id sent to the external auth provider.
	// Overridden per-request by the X-Org-ID header when present.
	AuthOrgID string

	// SkipPaths maps "METHOD /path" keys that bypass authentication.
	// Example: "GET /" → true allows unauthenticated GET on root.
	SkipPaths map[string]bool

	// OnAuth is an optional callback invoked on every auth decision.
	// success indicates whether the request was authenticated.
	OnAuth func(r *http.Request, success bool)
}

// authHTTPClient is a shared client with reasonable timeouts for auth requests.
var authHTTPClient = &http.Client{Timeout: 10 * time.Second}

// externalVerifyRequest is the request body sent to the external auth URL.
type externalVerifyRequest struct {
	Token string `json:"token"`
	OrgID string `json:"org_id"`
}

// externalVerifyResponse is the response from the external auth URL.
type externalVerifyResponse struct {
	Valid       bool   `json:"valid"`
	Error       string `json:"error,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	OrgID       string `json:"org_id,omitempty"`
	Email       string `json:"email,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// validateExternal sends the bearer token to the external auth URL and returns
// whether the token is valid.
func validateExternal(authURL, token, orgID string) (bool, error) {
	body, err := json.Marshal(externalVerifyRequest{Token: token, OrgID: orgID})
	if err != nil {
		return false, fmt.Errorf("marshalling auth request: %w", err)
	}

	resp, err := authHTTPClient.Post(authURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("calling auth URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("auth URL returned status %d", resp.StatusCode)
	}

	var result externalVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decoding auth response: %w", err)
	}

	return result.Valid, nil
}

// DefaultSkipPaths returns the default set of public endpoints
// that do not require authentication (agent card, health checks).
func DefaultSkipPaths() map[string]bool {
	return map[string]bool{
		"GET /":                           true,
		"GET /.well-known/agent.json":     true,
		"GET /healthz":                    true,
		"GET /health":                     true,
		"OPTIONS /":                       true,
		"OPTIONS /.well-known/agent.json": true,
		"OPTIONS /healthz":                true,
		"OPTIONS /health":                 true,
	}
}

// errorResponse is the JSON body returned for auth failures.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Middleware returns an http.Handler that enforces bearer token authentication.
// If cfg.Enabled is false, requests pass through without checks.
// When cfg.AuthURL is set, tokens are validated against the external provider.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Check if this method+path combination is public.
			key := r.Method + " " + r.URL.Path
			if cfg.SkipPaths[key] {
				next.ServeHTTP(w, r)
				return
			}

			// Extract bearer token from Authorization header.
			token := extractBearerToken(r)
			if token == "" {
				authFail(w, r, cfg, "valid bearer token required")
				return
			}

			// External auth provider validation.
			if cfg.AuthURL != "" {
				// Use org_id from request header (multiple header names), fall back to config.
				orgID := extractOrgID(r, cfg.AuthOrgID)
				valid, err := validateExternal(cfg.AuthURL, token, orgID)
				if err != nil {
					authFail(w, r, cfg, "auth provider error")
					return
				}
				if valid {
					if cfg.OnAuth != nil {
						cfg.OnAuth(r, true)
					}
					next.ServeHTTP(w, r)
					return
				}
				authFail(w, r, cfg, "token rejected by auth provider")
				return
			}

			// Local token validation.
			if ValidateToken(token, cfg.Token) {
				if cfg.OnAuth != nil {
					cfg.OnAuth(r, true)
				}
				next.ServeHTTP(w, r)
				return
			}

			authFail(w, r, cfg, "valid bearer token required")
		})
	}
}

// authFail sends a 401 response and fires the OnAuth callback.
func authFail(w http.ResponseWriter, r *http.Request, cfg Config, msg string) {
	if cfg.OnAuth != nil {
		cfg.OnAuth(r, false)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(errorResponse{ //nolint:errcheck
		Error:   "unauthorized",
		Message: msg,
	})
}

// extractOrgID reads the org ID from the request headers, checking multiple
// common header names: "X-Org-ID", "org-id", "org_id". Falls back to the
// provided default if none are set.
func extractOrgID(r *http.Request, fallback string) string {
	for _, h := range []string{"X-Org-ID", "org-id", "org_id"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return fallback
}

// extractBearerToken extracts the token from "Authorization: Bearer <token>".
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return auth[len(prefix):]
	}
	return ""
}
