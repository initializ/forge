package gcp_iap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/initializ/forge/forge-core/auth"
)

// IAPClaims is the projected claim set Forge needs from an IAP JWT.
// IAP-emitted JWTs include several other claims (hd, exp, iat, ...);
// callers can retrieve them from the raw token if needed.
type IAPClaims struct {
	Issuer   string
	Audience []string
	Subject  string
	Email    string
	HD       string // Google Workspace domain (optional)
}

// IAPJWKSCache fetches and caches the IAP public keys. ES256-only —
// dropping non-EC / non-P-256 / non-ES256-labeled keys during parse
// is a defense-in-depth layer on top of the algorithm whitelist in
// VerifyAndParse.
//
// Refresh model (mirrors Phase 1 OIDC review #1):
//   - lastSuccessful tracks the TTL window; reuse cached keys within it.
//   - lastAttempt + backoffDuration block fetch stampedes during outages.
//   - Stale-grace: if backoff blocks fetch AND a key for the requested
//     kid is in cache, return the stale key. IAP rotates keys on the
//     order of weeks, so freshness matters less than availability.
type IAPJWKSCache struct {
	url  string
	ttl  time.Duration
	http *http.Client

	mu              sync.RWMutex
	keys            map[string]*ecdsa.PublicKey
	lastSuccessful  time.Time
	lastAttempt     time.Time
	backoffDuration time.Duration
}

// NewIAPJWKSCache builds an empty cache pointing at the given URL.
//
// CheckRedirect is pinned to ErrUseLastResponse. The IAP JWKS host is
// hardcoded (decision §9.4) precisely so we never trust any other source
// of public keys — auto-following a 3xx to a foreign URL would let a
// MITM / TLS-inspecting proxy / DNS-hijack scenario substitute attacker
// keys, after which any forged token signed by those keys would verify.
// Refuse redirects outright.
func NewIAPJWKSCache(url string, ttl, timeout time.Duration) *IAPJWKSCache {
	return &IAPJWKSCache{
		url: url,
		ttl: ttl,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		keys: map[string]*ecdsa.PublicKey{},
	}
}

// VerifyAndParse validates the JWT signature against the cached IAP
// public keys and returns the projected claims. Standard claim checks
// (exp/iat/nbf) are performed by the JWT library; iss/aud are checked
// by the caller (Provider.Verify) against the operator's config.
func (j *IAPJWKSCache) VerifyAndParse(ctx context.Context, raw string) (*IAPClaims, error) {
	tok, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		// Algorithm whitelist: IAP signs with ES256 only. Any other alg
		// is rejected BEFORE key lookup so algorithm-confusion attacks
		// can't reach the JWKS.
		if t.Method.Alg() != "ES256" {
			return nil, fmt.Errorf("unexpected alg %q", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("missing kid")
		}
		key, err := j.keyForKID(ctx, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"ES256"}))

	if err != nil {
		return nil, classifyJWTErr(err)
	}
	if !tok.Valid {
		return nil, fmt.Errorf("%w: jwt.Valid=false", auth.ErrInvalidToken)
	}

	// Extract claims via a small intermediate struct so we can handle
	// the "aud as string OR array" shape (IAP currently uses string).
	payload, err := json.Marshal(tok.Claims)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal claims: %v", auth.ErrInvalidToken, err)
	}
	var rc struct {
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
		Subject  string          `json:"sub"`
		Email    string          `json:"email"`
		HD       string          `json:"hd"`
	}
	if err := json.Unmarshal(payload, &rc); err != nil {
		return nil, fmt.Errorf("%w: unmarshal claims: %v", auth.ErrInvalidToken, err)
	}
	aud, err := parseAudience(rc.Audience)
	if err != nil {
		return nil, fmt.Errorf("%w: aud parse: %v", auth.ErrInvalidToken, err)
	}
	return &IAPClaims{
		Issuer:   rc.Issuer,
		Audience: aud,
		Subject:  rc.Subject,
		Email:    rc.Email,
		HD:       rc.HD,
	}, nil
}

// parseAudience handles "aud" being either a JSON string or an array.
// JWT spec allows either; IAP currently uses string.
func parseAudience(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("aud claim missing")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return []string{s}, nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

// keyForKID returns the ES256 public key for the given kid. Refreshes
// the JWKS in the foreground when the cache is stale or a kid isn't
// known, with backoff + stale-grace per the IAPJWKSCache contract.
func (j *IAPJWKSCache) keyForKID(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	j.mu.RLock()
	cached, hit := j.keys[kid]
	stale := time.Since(j.lastSuccessful) > j.ttl
	j.mu.RUnlock()

	if hit && !stale {
		return cached, nil
	}

	if err := j.refresh(ctx); err != nil {
		if hit {
			// Stale-grace: keep using the cached key during outage.
			return cached, nil
		}
		return nil, err
	}

	j.mu.RLock()
	k := j.keys[kid]
	j.mu.RUnlock()
	if k == nil {
		return nil, fmt.Errorf("%w: kid %q not found in IAP JWKS", auth.ErrInvalidToken, kid)
	}
	return k, nil
}

// refresh fetches and parses the JWKS. Backoff doubles on each failure
// (5s → 60s cap); resets on success.
func (j *IAPJWKSCache) refresh(ctx context.Context) error {
	j.mu.Lock()
	if !j.lastAttempt.IsZero() && time.Since(j.lastAttempt) < j.backoffDuration {
		j.mu.Unlock()
		return fmt.Errorf("%w: IAP JWKS in backoff", auth.ErrProviderUnavailable)
	}
	j.lastAttempt = time.Now()
	j.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		j.bumpBackoff()
		return fmt.Errorf("%w: IAP JWKS build request: %v", auth.ErrProviderUnavailable, err)
	}
	resp, err := j.http.Do(req)
	if err != nil {
		j.bumpBackoff()
		return fmt.Errorf("%w: IAP JWKS fetch: %v", auth.ErrProviderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		j.bumpBackoff()
		return fmt.Errorf("%w: IAP JWKS HTTP %d", auth.ErrProviderUnavailable, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		j.bumpBackoff()
		return fmt.Errorf("%w: IAP JWKS read: %v", auth.ErrProviderUnavailable, err)
	}

	keys, err := parseECJWKSet(raw)
	if err != nil {
		j.bumpBackoff()
		return fmt.Errorf("%w: IAP JWKS parse: %v", auth.ErrProviderUnavailable, err)
	}

	// Merge-on-success rather than replace (Review NIT). A partial-
	// but-valid JWKS (e.g. one freshly-rotated kid omitted by mistake)
	// must not drop kids the stale-grace contract assumes we still
	// have. New keys take precedence over old of the same kid; old
	// keys that aren't in the new response are kept.
	//
	// Worst case: a stale key for a kid GCP has actually retired stays
	// in our cache. JWT signature verification will fail naturally for
	// any token signed with the retired private key, so this can't
	// admit forged tokens — it just keeps verification working through
	// JWKS-API hiccups.
	j.mu.Lock()
	if j.keys == nil {
		j.keys = map[string]*ecdsa.PublicKey{}
	}
	for kid, k := range keys {
		j.keys[kid] = k
	}
	j.lastSuccessful = time.Now()
	j.backoffDuration = 0
	j.mu.Unlock()
	return nil
}

func (j *IAPJWKSCache) bumpBackoff() {
	j.mu.Lock()
	switch {
	case j.backoffDuration == 0:
		j.backoffDuration = 5 * time.Second
	case j.backoffDuration < 60*time.Second:
		j.backoffDuration *= 2
	}
	j.mu.Unlock()
}

// parseECJWKSet drops keys that aren't EC/P-256/ES256. Defense in depth
// against a compromised JWKS endpoint trying to slip in RSA keys for
// algorithm-confusion attacks.
func parseECJWKSet(raw []byte) (map[string]*ecdsa.PublicKey, error) {
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, err
	}
	out := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" {
			continue
		}
		if k.Alg != "" && k.Alg != "ES256" {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			continue
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			continue
		}
		out[k.Kid] = &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}
	}
	if len(out) == 0 {
		return nil, errors.New("IAP JWKS contained no usable ES256 keys")
	}
	return out, nil
}

// classifyJWTErr maps golang-jwt errors to auth sentinels using v5's
// named sentinels via errors.Is rather than substring matching. The
// library's error wording can shift across patch releases; the sentinels
// are part of its public API and stable. (Review NIT.)
//
// Two-tier mapping:
//
//	ErrInvalidToken (malformed / structurally wrong):
//	  - jwt.ErrTokenMalformed         (bad base64, dot-count, etc.)
//	  - jwt.ErrTokenUnverifiable      (no key found, alg mismatch
//	                                   detected in keyFunc)
//	  - jwt.ErrInvalidKey, jwt.ErrInvalidKeyType
//	  - keyFunc messages we emit ourselves ("unexpected alg",
//	    "missing kid", "not found in IAP JWKS")
//
//	ErrTokenRejected (well-formed but cryptographically/temporally
//	                  invalid — policy-denial shape):
//	  - jwt.ErrTokenSignatureInvalid
//	  - jwt.ErrTokenExpired
//	  - jwt.ErrTokenNotValidYet (nbf in future)
//	  - jwt.ErrTokenUsedBeforeIssued (iat in future)
//	  - jwt.ErrTokenInvalidClaims
//
// Default: ErrInvalidToken (conservative — unknown errors are
// classified as malformed, not as policy rejections).
func classifyJWTErr(err error) error {
	if errors.Is(err, auth.ErrProviderUnavailable) ||
		errors.Is(err, auth.ErrInvalidToken) ||
		errors.Is(err, auth.ErrTokenRejected) {
		return err
	}

	// Special case: ErrTokenSignatureInvalid wraps BOTH (a) actual
	// bad-signature failures (rejected) AND (b) alg-confusion errors
	// where golang-jwt's WithValidMethods refused the token's alg
	// before signing was even attempted. The latter is a malformed-
	// shape failure (alg whitelist tripped), not a policy denial, so
	// inspect the wrapped message to distinguish.
	if errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		s := err.Error()
		if strings.Contains(s, "signing method") {
			return fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		return fmt.Errorf("%w: %v", auth.ErrTokenRejected, err)
	}

	switch {
	case errors.Is(err, jwt.ErrTokenMalformed),
		errors.Is(err, jwt.ErrTokenUnverifiable),
		errors.Is(err, jwt.ErrInvalidKey),
		errors.Is(err, jwt.ErrInvalidKeyType):
		return fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	case errors.Is(err, jwt.ErrTokenExpired),
		errors.Is(err, jwt.ErrTokenNotValidYet),
		errors.Is(err, jwt.ErrTokenUsedBeforeIssued),
		errors.Is(err, jwt.ErrTokenInvalidClaims):
		return fmt.Errorf("%w: %v", auth.ErrTokenRejected, err)
	}

	// keyFunc errors we emit ourselves get wrapped by jwt.Parse, but
	// the unwrap chain doesn't preserve them as distinct sentinels.
	// Fall back to substring matching for THESE messages only — they're
	// strings WE control, not the library's.
	s := err.Error()
	switch {
	case strings.Contains(s, "unexpected alg"),
		strings.Contains(s, "missing kid"),
		strings.Contains(s, "not found in IAP JWKS"):
		return fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	return fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
}
