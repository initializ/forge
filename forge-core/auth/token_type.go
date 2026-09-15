package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// RFC 8725 explicit token typing (agent-identity, #444 item 5).
//
// The platform mints JWTs with an explicit `typ` header naming an initializ
// media type (api-next#36). Forge enforces the discipline on the INBOUND path:
// a token minted for a different purpose — a chain token, a workload
// credential, a mandate — must be REJECTED where an access token is expected,
// so a token issued for one leg can't be replayed as a caller's access token
// (a token-confusion / cross-use attack, RFC 8725 §2.8 / §3.11).
const (
	// MediaTypePlatformBearer is the valid inbound access-token type. Accepted.
	MediaTypePlatformBearer = "application/vnd.initializ.platform-bearer+jwt"
	// MediaTypeChainToken is an agent-to-agent chain token (#444 item 3).
	MediaTypeChainToken = "application/vnd.initializ.chain-token+jwt"
	// MediaTypeWorkloadCredential is the projected workload credential (#444 item 1).
	MediaTypeWorkloadCredential = "application/vnd.initializ.workload-credential+jwt"
	// MediaTypeMandate is an L2 delegation mandate object.
	MediaTypeMandate = "application/vnd.initializ.mandate+jwt"
)

// rejectedInboundTokenTypes are the initializ JWT media types that must never
// be accepted as an inbound access token. Denylist by design: an absent /
// unknown / platform-bearer `typ` passes through to normal verification, so
// existing tokens (and any third-party OIDC token, which carries no initializ
// typ) are unaffected — only a token that explicitly declares one of these
// non-access purposes is refused.
//
// CROSS-REPO CONTRACT — keep in sync with the platform's own ingress guard,
// api-next `helper/tokentype.go` (`ourNonPlatformTypes` / `RejectForeignTokenClass`).
// The two denylists must name the SAME non-access classes with exact-match
// semantics; they match as of api-next develop. There is no shared source of
// truth across the repos, so when api-next adds a fourth non-access media type
// this set MUST gain it in lockstep — otherwise forge silently keeps accepting
// that class as an access token, reopening the cross-use gap. Drift is tracked
// on the #444 enforcement epic.
var rejectedInboundTokenTypes = map[string]bool{
	MediaTypeChainToken:         true,
	MediaTypeWorkloadCredential: true,
	MediaTypeMandate:            true,
}

// jwtHeaderTyp decodes — WITHOUT verifying the signature — the `typ` header of
// a compact-JWS token, returning "" when token is not a well-formed JWT or
// carries no typ. No signature check is needed for a reject decision: if the
// header declares a non-access purpose, the token must be refused regardless of
// whether its signature would validate.
func jwtHeaderTyp(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	// JWS uses base64url without padding (RFC 7515 §2).
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	var hdr struct {
		Typ string `json:"typ"`
	}
	if json.Unmarshal(raw, &hdr) != nil {
		return ""
	}
	return hdr.Typ
}

// IsRejectedInboundTokenType reports whether a bearer token explicitly declares
// (via its JWT `typ` header) one of the non-access initializ media types that
// must be rejected where an access token is expected (#444 item 5). Non-JWT,
// no-typ, unknown-typ, and platform-bearer tokens return false.
func IsRejectedInboundTokenType(token string) bool {
	return rejectedInboundTokenTypes[jwtHeaderTyp(token)]
}
