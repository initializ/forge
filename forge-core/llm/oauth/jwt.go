package oauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// ParseJWTExp decodes — WITHOUT verifying the signature — the `exp` claim of a
// compact-JWS token and returns it as a time. ok is false when token is not a
// well-formed JWT or carries no (numeric) exp, in which case the returned time
// is the zero value.
//
// The apiKeyHelper gateway credential (#455) uses this to cache a helper-minted
// token until its real expiry. A non-JWT / opaque token → ok=false → the caller
// treats it as immediately expired and re-runs the helper on every call (safe:
// it just forgoes caching). No signature check is needed for a cache decision —
// the token is the gateway's to validate, not ours.
func ParseJWTExp(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	// JWS uses base64url without padding (RFC 7515 §2); tolerate padded
	// encoders by trimming any '=' before RawURLEncoding.
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}
