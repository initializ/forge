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
	// Chain is the provider chain that verifies bearer tokens. May only
	// be nil when AllowAnonymous is true (see below).
	Chain Provider

	// AllowAnonymous explicitly opts the middleware into running without
	// authentication. Required whenever Chain is nil — otherwise
	// Middleware() panics at construction. This prevents a misconfigured
	// runner from silently serving unauthenticated requests because
	// someone forgot to wire a chain.
	//
	// Set this to true when:
	//   - --no-auth flag is in effect (operator explicitly chose anon)
	//   - No auth: block AND no --auth-url AND no channels (legacy local
	//     dev default — preserved for backward compat)
	//
	// Leave this false for any production deployment that intends to
	// enforce auth; a nil chain will then panic loudly at startup
	// instead of running open.
	AllowAnonymous bool

	// SkipPaths maps "METHOD /path" keys that bypass authentication.
	// If nil, DefaultSkipPaths() is used.
	SkipPaths map[string]bool

	// OnAuth is an optional callback invoked on every auth decision.
	//
	//   - identity is non-nil and err is nil on success.
	//   - identity is nil and err carries the chain error on failure
	//     (or auth.ErrMissingBearer when the header was absent).
	//   - tokenKind is "jwt", "opaque", or "empty" — structural metadata
	//     safe to log. The token itself is NOT passed; callers must not
	//     try to recover it from the request.
	//
	// Callbacks should be cheap — they run on the request hot path.
	OnAuth func(r *http.Request, identity *Identity, err error, tokenKind string)
}

// ErrMissingBearer is returned (via OnAuth) when the request lacked an
// Authorization: Bearer ... header. Distinct from chain-level errors so
// callers can emit a precise "missing_token" reason code without parsing
// error strings.
var ErrMissingBearer = errorString("auth: missing bearer token")

// errorString is a sentinel-friendly error type that compares by identity.
type errorString string

func (e errorString) Error() string { return string(e) }

// Middleware returns an http.Handler that enforces bearer token authentication
// via the provided Provider chain.
//
// Panics at construction if opts.Chain is nil and opts.AllowAnonymous is
// false. This is intentional — silently passing through requests when the
// caller forgot to wire a chain is the highest-impact misconfiguration in
// the auth subsystem (open prod endpoint). Fail-loud catches it at startup,
// not at the first request from a real user.
func Middleware(opts MiddlewareOptions) func(http.Handler) http.Handler {
	if opts.Chain == nil {
		if !opts.AllowAnonymous {
			panic("auth: Middleware called with nil Chain and AllowAnonymous=false. " +
				"Set MiddlewareOptions.AllowAnonymous: true to explicitly allow " +
				"unauthenticated access, or provide a Chain.")
		}
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
			kind := TokenKind(token)

			if token == "" {
				notifyAuth(opts.OnAuth, r, nil, ErrMissingBearer, kind)
				writeAuthError(w, "valid bearer token required")
				return
			}

			identity, err := opts.Chain.Verify(r.Context(), token, HeadersFromRequest(r))
			if err != nil || identity == nil {
				notifyAuth(opts.OnAuth, r, nil, err, kind)
				writeAuthError(w, classifyAuthFailure(err))
				return
			}

			notifyAuth(opts.OnAuth, r, identity, nil, kind)

			ctx := WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// notifyAuth invokes the OnAuth callback if set, swallowing the nil check
// at the call sites so the main middleware body stays readable.
func notifyAuth(cb func(*http.Request, *Identity, error, string), r *http.Request, id *Identity, err error, kind string) {
	if cb == nil {
		return
	}
	cb(r, id, err, kind)
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
	case errors.Is(err, ErrProviderUnavailable):
		// Surface a distinct user-visible message so retry behavior on
		// the client can be different from "invalid token". This is also
		// the operator-facing signal in /healthz-style probes.
		return "auth provider unavailable"
	default:
		return "auth provider error"
	}
}

// writeAuthError sends a 401 JSON response. The OnAuth callback is fired
// separately (via notifyAuth) so the audit-emission path can run with the
// full Identity / error context, not just a bool.
func writeAuthError(w http.ResponseWriter, msg string) {
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
