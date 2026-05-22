package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// errorResponse is the JSON body returned for auth failures.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
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

// MiddlewareOptions configures Middleware.
type MiddlewareOptions struct {
	// Chain is the provider chain that verifies bearer tokens. If nil,
	// the middleware behaves as a passthrough (anonymous access).
	Chain Provider

	// SkipPaths maps "METHOD /path" keys that bypass authentication.
	// If nil, DefaultSkipPaths() is used.
	SkipPaths map[string]bool

	// OnAuth is an optional callback invoked on every auth decision.
	// success is true when the request authenticated; false otherwise.
	OnAuth func(r *http.Request, success bool)
}

// Middleware returns an http.Handler that enforces bearer token authentication
// via the provided Provider chain. If opts.Chain is nil, requests pass through
// without checks (anonymous access).
func Middleware(opts MiddlewareOptions) func(http.Handler) http.Handler {
	if opts.Chain == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	skip := opts.SkipPaths
	if skip == nil {
		skip = DefaultSkipPaths()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip[r.Method+" "+r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			token := extractBearerToken(r)
			if token == "" {
				writeAuthFail(w, r, opts.OnAuth, "valid bearer token required")
				return
			}

			identity, err := opts.Chain.Verify(r.Context(), token, HeadersFromRequest(r))
			if err != nil || identity == nil {
				writeAuthFail(w, r, opts.OnAuth, classifyAuthFailure(err))
				return
			}

			if opts.OnAuth != nil {
				opts.OnAuth(r, true)
			}

			ctx := WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// classifyAuthFailure maps a chain error into a user-visible message.
// Keep messages generic to avoid leaking information about which provider
// rejected the token or why.
func classifyAuthFailure(err error) string {
	switch {
	case err == nil, errors.Is(err, ErrTokenNotForMe):
		return "valid bearer token required"
	case errors.Is(err, ErrTokenRejected):
		return "token rejected by auth provider"
	case errors.Is(err, ErrInvalidToken):
		return "invalid token"
	default:
		return "auth provider error"
	}
}

// writeAuthFail sends a 401 response and fires the OnAuth callback if set.
func writeAuthFail(w http.ResponseWriter, r *http.Request, onAuth func(*http.Request, bool), msg string) {
	if onAuth != nil {
		onAuth(r, false)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   "unauthorized",
		Message: msg,
	})
}

// extractBearerToken extracts the token from "Authorization: Bearer <token>".
// Case-insensitive on the "Bearer" prefix to preserve historical behavior.
func extractBearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	return ""
}
