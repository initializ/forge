package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Config controls bearer-token authentication for the A2A server.
type Config struct {
	// Enabled controls whether authentication is enforced.
	Enabled bool

	// Token is the expected bearer token value.
	Token string

	// SkipPaths maps "METHOD /path" keys that bypass authentication.
	// Example: "GET /" → true allows unauthenticated GET on root.
	SkipPaths map[string]bool

	// OnAuth is an optional callback invoked on every auth decision.
	// success indicates whether the request was authenticated.
	OnAuth func(r *http.Request, success bool)
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
			if ValidateToken(token, cfg.Token) {
				if cfg.OnAuth != nil {
					cfg.OnAuth(r, true)
				}
				next.ServeHTTP(w, r)
				return
			}

			// Authentication failed.
			if cfg.OnAuth != nil {
				cfg.OnAuth(r, false)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(errorResponse{ //nolint:errcheck
				Error:   "unauthorized",
				Message: "valid bearer token required",
			})
		})
	}
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
