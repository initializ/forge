package aws_sigv4

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TokenPrefix is the magic token-type marker that distinguishes
// forge-aws-v1 tokens from JWTs / opaque tokens in the Bearer slot.
// Mirrors the "k8s-aws-v1." convention from aws-iam-authenticator.
const TokenPrefix = "forge-aws-v1."

// PresignedToken is the parsed view of a forge-aws-v1 Bearer token.
// The token wraps a pre-signed STS GetCallerIdentity URL; AKID + Date are
// extracted from the URL's X-Amz-Credential query param so the provider's
// identity cache can key on them without re-deriving for every Verify call.
//
// RawURL holds the original URL byte-for-byte as it appeared in the
// decoded token payload. Forge MUST use RawURL when invoking STS — round-
// tripping through Go's net/url package re-encodes query parameters in
// ways that differ from how the AWS SDK emitted them (e.g., percent-
// encoding of "/" in X-Amz-Credential, "+" in X-Amz-Security-Token), and
// any such re-encoding invalidates the signature.
type PresignedToken struct {
	RawURL  string        // the exact URL the AWS SDK produced — preserve as-is
	URL     *url.URL      // parsed view, for host validation and query inspection only
	AKID    string        // for IdentityCache bucket key
	Date    string        // YYYYMMDD scope date — for IdentityCache bucket key
	Region  string        // from the credential scope (we cross-check against cfg.Region)
	SigTime time.Time     // parsed X-Amz-Date — used by CheckFreshness, not by ParseToken itself
	Expires time.Duration // parsed X-Amz-Expires — used by CheckFreshness
}

// ParseToken validates a forge-aws-v1 Bearer token end-to-end and returns
// the URL Forge should invoke on STS.
//
// expectedHost is sts.<region>.amazonaws.com for prod, or the test-mode
// override host (Config.STSEndpoint) for integration tests.
//
// Validation gates (in order — fail-fast):
//
//  1. Token starts with the TokenPrefix.
//  2. Body decodes as base64url.
//  3. Decoded payload parses as a URL.
//  4. URL scheme is https (or http when STSEndpoint test override is in use).
//  5. URL host matches expectedHost.
//  6. URL query has Action=GetCallerIdentity.
//  7. URL query has X-Amz-Algorithm=AWS4-HMAC-SHA256.
//  8. URL query has a non-empty X-Amz-Signature.
//  9. URL query has X-Amz-Credential parseable as AKID/YYYYMMDD/region/sts/aws4_request.
//
// Returns ErrTokenNotForMe only when (1) fails — the prefix is the only
// "shape" check; everything else is a malformed / rejected token from
// our perspective, classified as ErrInvalidToken or ErrTokenRejected
// by the caller.
func ParseToken(token, expectedHost string, requireHTTPS bool) (*PresignedToken, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, errors.New("missing forge-aws-v1 prefix")
	}
	encoded := strings.TrimPrefix(token, TokenPrefix)
	if encoded == "" {
		return nil, errors.New("forge-aws-v1 token has empty payload")
	}

	// base64url decode — accept both padded and unpadded forms because
	// SDKs disagree on whether to emit "=" padding.
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		// fallback to standard base64url with padding
		raw, err = base64.URLEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("base64url decode: %w", err)
		}
	}

	u, err := url.Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("decoded payload is not a URL: %w", err)
	}

	if requireHTTPS && u.Scheme != "https" {
		return nil, fmt.Errorf("URL scheme %q is not https", u.Scheme)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("URL scheme %q is not http(s)", u.Scheme)
	}
	// Reject userinfo BEFORE the host check. RFC 3986 separates
	// userinfo from host, so net/url parses
	// "https://user:pass@sts.us-east-1.amazonaws.com" into
	// (u.User="user:pass", u.Host="sts.us-east-1.amazonaws.com") —
	// the host check alone would let that token through. Then
	// http.Client.Do would synthesize Authorization: Basic <b64> from
	// u.User and ship attacker-controlled bytes to STS. STS ignores
	// Basic (it uses the X-Amz-Signature query param), but we still
	// don't want attacker bytes leaving the box. (Review M1.)
	if u.User != nil {
		return nil, errors.New("URL must not contain userinfo (RFC 3986 user:pass@ section)")
	}
	if !strings.EqualFold(u.Host, expectedHost) {
		return nil, fmt.Errorf("URL host %q does not match expected %q (SSRF guard)", u.Host, expectedHost)
	}

	q := u.Query()
	if q.Get("Action") != "GetCallerIdentity" {
		return nil, fmt.Errorf("URL Action=%q, want GetCallerIdentity", q.Get("Action"))
	}
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		return nil, fmt.Errorf("URL X-Amz-Algorithm=%q, want AWS4-HMAC-SHA256", q.Get("X-Amz-Algorithm"))
	}
	if q.Get("X-Amz-Signature") == "" {
		return nil, errors.New("URL missing X-Amz-Signature")
	}

	akid, date, region, err := parseCredentialScope(q.Get("X-Amz-Credential"))
	if err != nil {
		return nil, fmt.Errorf("X-Amz-Credential: %w", err)
	}

	sigTime, err := parseAmzDate(q.Get("X-Amz-Date"))
	if err != nil {
		return nil, fmt.Errorf("X-Amz-Date: %w", err)
	}
	expires, err := parseAmzExpires(q.Get("X-Amz-Expires"))
	if err != nil {
		return nil, fmt.Errorf("X-Amz-Expires: %w", err)
	}

	return &PresignedToken{
		RawURL:  string(raw),
		URL:     u,
		AKID:    akid,
		Date:    date,
		Region:  region,
		SigTime: sigTime,
		Expires: expires,
	}, nil
}

// CheckFreshness rejects tokens whose self-declared lifetime exceeds
// maxExpires OR whose validity window has already lapsed (with skew
// for clock drift between Forge and the caller). This is defense in
// depth on top of STS's own ~15min enforcement: if STS ever accepts
// a stale token, our IdentityCache would happily serve the cached
// Identity for its full TTL. Parser-side freshness closes that gap.
//
// Caller passes `now` and the limits so this is unit-testable without
// time monkey-patching. Provider supplies them from its Config.
func (t *PresignedToken) CheckFreshness(now time.Time, maxExpires, skew time.Duration) error {
	if t.Expires > maxExpires {
		return fmt.Errorf("X-Amz-Expires=%s exceeds cap %s", t.Expires, maxExpires)
	}
	// Token's own self-declared expiry passed already (with skew tolerance).
	if now.After(t.SigTime.Add(t.Expires).Add(skew)) {
		return fmt.Errorf("token expired: signed at %s + %s lifetime + %s skew, now %s",
			t.SigTime.UTC().Format(time.RFC3339), t.Expires, skew, now.UTC().Format(time.RFC3339))
	}
	// Token from the future beyond our skew tolerance — either a wildly
	// skewed client OR a malicious signer trying to extend the validity
	// window. STS itself catches this; we belt-and-brace.
	if t.SigTime.Sub(now) > skew {
		return fmt.Errorf("token signed in the future: %s vs now %s (skew %s)",
			t.SigTime.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), skew)
	}
	return nil
}

// parseAmzDate parses an X-Amz-Date timestamp in its standard form
// "YYYYMMDDTHHMMSSZ" (e.g. "20260524T150405Z"). UTC by definition.
func parseAmzDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("missing X-Amz-Date")
	}
	t, err := time.Parse("20060102T150405Z", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("malformed %q: %v", s, err)
	}
	return t, nil
}

// parseAmzExpires parses the X-Amz-Expires query value (seconds, as a
// decimal integer string). AWS SDKs constrain this to [1, 604800]
// (1s to 7 days) at signing time; we additionally cap at CheckFreshness
// time per the operator's maxExpires.
func parseAmzExpires(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("missing X-Amz-Expires")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not an integer: %q", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive, got %d", n)
	}
	return time.Duration(n) * time.Second, nil
}

// parseCredentialScope splits X-Amz-Credential into its five segments:
//
//	AKID/YYYYMMDD/region/service/aws4_request
//
// Service MUST be "sts" and the tail MUST be "aws4_request".
func parseCredentialScope(cred string) (akid, date, region string, err error) {
	if cred == "" {
		return "", "", "", errors.New("missing X-Amz-Credential")
	}
	segs := strings.Split(cred, "/")
	if len(segs) != 5 {
		return "", "", "", fmt.Errorf("expected 5 /-separated parts, got %d", len(segs))
	}
	if segs[3] != "sts" {
		return "", "", "", fmt.Errorf("scope service=%q, want sts", segs[3])
	}
	if segs[4] != "aws4_request" {
		return "", "", "", fmt.Errorf("scope tail=%q, want aws4_request", segs[4])
	}
	if segs[0] == "" || segs[1] == "" || segs[2] == "" {
		return "", "", "", errors.New("empty AKID/date/region segment")
	}
	return segs[0], segs[1], segs[2], nil
}
