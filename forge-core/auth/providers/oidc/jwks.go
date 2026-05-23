package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// jwk is one entry of a JWKS document. We only model the fields we read.
//
// Reference: RFC 7517 (JWK) and RFC 7518 (JWA).
type jwk struct {
	Kty string `json:"kty"`           // key type ("RSA" or "EC")
	Use string `json:"use,omitempty"` // "sig" expected
	Alg string `json:"alg,omitempty"` // algorithm hint (advisory)
	Kid string `json:"kid,omitempty"` // key ID

	// RSA fields
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`

	// EC fields
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

// parsedKey is a JWK after decoding the public key material plus the
// algorithm advertised by the JWKS. The provider trusts the JWKS-declared
// algorithm — NOT the algorithm asserted in the token header.
type parsedKey struct {
	Public any    // *rsa.PublicKey or *ecdsa.PublicKey
	Alg    string // RS256, ES256, etc.
}

// jwksCache is a TTL-bounded cache of parsed JWK keys keyed by `kid`.
//
// Refresh strategy:
//
//  1. First lookup ever: fetch JWKS.
//  2. TTL expired (now - lastSuccessful > ttl): fetch JWKS on the request
//     path. This is what invalidates keys that the IdP has revoked —
//     without it, a compromised key removed from the issuer's JWKS would
//     remain trusted by Forge until process restart.
//  3. kid not in cache (and TTL not expired): fetch JWKS to pick up new
//     keys an IdP may have added since last refresh.
//  4. kid still not in cache after refresh: return ErrKeyNotFound.
//  5. kid in cache and TTL not expired: return immediately (hot path).
//
// Two separate timestamps track refresh state so failures don't extend the
// TTL window:
//   - lastSuccessful: timestamp of the most recent successful refresh.
//     Drives TTL accounting; only advanced on success.
//   - lastAttempt: timestamp of the most recent attempt (success or fail).
//     Drives failure backpressure so a dead JWKS endpoint isn't hammered.
//
// Concurrency: a single fetch is in flight at a time; concurrent misses
// coalesce behind the write lock.
type jwksCache struct {
	jwksURL    func(ctx context.Context) (string, error)
	httpClient *http.Client
	ttl        time.Duration
	// refetchGrace prevents a malformed kid from triggering one JWKS fetch
	// per request. Within this window, repeated unknown-kid lookups skip
	// the refetch.
	refetchGrace time.Duration
	// now returns the current time. Injectable for tests so TTL expiry
	// can be exercised without real time passing.
	now func() time.Time

	mu             sync.RWMutex
	keys           map[string]parsedKey
	lastSuccessful time.Time // last successful JWKS load — drives TTL
	lastAttempt    time.Time // last refresh attempt (success or fail) — drives error backoff
	// lastMissRefresh is the last time we refreshed *because of* an
	// unknown kid and the kid was still not present after refresh.
	// Used to suppress stampedes of refetches for the same bad kid.
	lastMissRefresh time.Time
	lastErr         error // last fetch error, for diagnostics
}

const (
	defaultJWKSTTL      = 1 * time.Hour
	minJWKSTTL          = 5 * time.Minute
	defaultRefetchGrace = 30 * time.Second
)

func newJWKSCache(jwksURL func(ctx context.Context) (string, error), client *http.Client, ttl time.Duration) *jwksCache {
	if ttl == 0 {
		ttl = defaultJWKSTTL
	}
	// Only clamp the minimum when a TTL was actually requested — leaving
	// callers to opt out of TTL entirely would require an explicit zero
	// (which the previous branch promoted to the default). The clamp
	// protects against operators setting an absurdly short value that
	// would hammer the IdP.
	if ttl < minJWKSTTL {
		ttl = minJWKSTTL
	}
	return &jwksCache{
		jwksURL:      jwksURL,
		httpClient:   client,
		ttl:          ttl,
		refetchGrace: defaultRefetchGrace,
		now:          time.Now,
		keys:         map[string]parsedKey{},
	}
}

// ttlExpired reports whether the cache's last successful refresh is older
// than the configured TTL. Returns true when there's no successful fetch
// yet (initial state). Must be called with at least an RLock held.
func (c *jwksCache) ttlExpired() bool {
	if c.lastSuccessful.IsZero() {
		return true
	}
	return c.now().Sub(c.lastSuccessful) > c.ttl
}

// backoffActive reports whether we should suppress a refresh attempt
// because a recent one failed. Prevents a dead JWKS endpoint from being
// hit on every request during an outage. Must be called with at least
// an RLock held.
func (c *jwksCache) backoffActive() bool {
	if c.lastErr == nil {
		return false
	}
	if c.lastAttempt.IsZero() {
		return false
	}
	return c.now().Sub(c.lastAttempt) < c.refetchGrace
}

// ErrKeyNotFound is returned when a `kid` is not in the cache and a
// refresh did not produce it.
var ErrKeyNotFound = fmt.Errorf("oidc: signing key not found")

// Get returns the parsed key for the given kid, refreshing the JWKS cache
// if necessary. Safe for concurrent use.
//
// Refresh semantics:
//   - TTL expired (cache stale): refresh on the request path. Required
//     for revocation to take effect — without this, a key removed from
//     the IdP's JWKS would remain trusted by Forge until process restart.
//   - kid in cache AND cache fresh: return immediately (hot path).
//   - kid missing AND not recently refreshed for a miss: refresh once.
//   - kid missing AND recently refreshed for a miss (within refetchGrace):
//     deny without re-fetching — prevents bad kids hammering the endpoint.
//   - last refresh failed AND within failure backoff: serve from existing
//     cache without re-attempt — protects a dead IdP from request storms.
func (c *jwksCache) Get(ctx context.Context, kid string) (parsedKey, error) {
	// Fast path: lock-free read. Treat a stale (TTL-expired) cache as if
	// every kid were a miss, so we fall into the write path below where
	// the actual refresh happens.
	c.mu.RLock()
	stale := c.ttlExpired()
	var (
		key parsedKey
		hit bool
	)
	if !stale {
		key, hit = c.keys[kid]
	}
	missCooldown := !c.lastMissRefresh.IsZero() && c.now().Sub(c.lastMissRefresh) < c.refetchGrace
	errBackoff := c.backoffActive()
	c.mu.RUnlock()

	if hit {
		return key, nil
	}
	// During error backoff we serve from the (possibly empty) cache —
	// don't re-attempt a known-broken endpoint. Note that on the very
	// first call (never-fetched cache + endpoint already broken), this
	// path is impossible because lastErr would still be nil.
	if errBackoff {
		return parsedKey{}, ErrKeyNotFound
	}
	// Don't refresh just to look up a kid we already concluded is absent.
	// TTL-expired cache always tries a refresh (security), regardless of
	// the unknown-kid grace window.
	if !stale && missCooldown {
		return parsedKey{}, ErrKeyNotFound
	}

	// Take the write lock so concurrent refresh attempts coalesce.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check under write lock — another goroutine may have refreshed
	// while we were waiting.
	if !c.ttlExpired() {
		if key, ok := c.keys[kid]; ok {
			return key, nil
		}
		if !c.lastMissRefresh.IsZero() && c.now().Sub(c.lastMissRefresh) < c.refetchGrace {
			return parsedKey{}, ErrKeyNotFound
		}
	}

	if err := c.refreshLocked(ctx); err != nil {
		return parsedKey{}, err
	}
	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	// Refresh happened but kid is still missing — remember that so we
	// don't hammer JWKS on the next unknown-kid request.
	c.lastMissRefresh = c.now()
	return parsedKey{}, ErrKeyNotFound
}

// refreshLocked fetches the JWKS endpoint and replaces the cache. Must be
// called with c.mu held for writing.
// refreshLocked fetches and parses the JWKS. lastAttempt is always
// updated (drives error backoff). lastSuccessful is updated ONLY when a
// full parse succeeds — failures must not extend the TTL window.
func (c *jwksCache) refreshLocked(ctx context.Context) error {
	c.lastAttempt = c.now()

	url, err := c.jwksURL(ctx)
	if err != nil {
		c.lastErr = err
		return fmt.Errorf("oidc: resolve jwks_uri: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.lastErr = err
		return fmt.Errorf("oidc: build jwks request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.lastErr = err
		return fmt.Errorf("oidc: jwks fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		err := fmt.Errorf("oidc: jwks returned status %d", resp.StatusCode)
		c.lastErr = err
		return err
	}

	var body struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		c.lastErr = err
		return fmt.Errorf("oidc: jwks decode: %w", err)
	}

	parsed := make(map[string]parsedKey, len(body.Keys))
	for _, k := range body.Keys {
		// Only signature-use keys. Empty `use` is permitted (treated as
		// "sig" since `key_ops` is not modeled).
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		// Reject HMAC algorithms — using shared-secret HMAC with JWKS is
		// a footgun: anyone who can read the JWKS can sign tokens.
		if k.Alg != "" && !isAllowedAlg(k.Alg) {
			continue
		}
		pk, alg, err := parseJWK(k)
		if err != nil {
			// Skip unparseable keys rather than failing the whole load —
			// real-world JWKS may include keys for algorithms we don't
			// support (e.g., RSA-OAEP for encryption).
			continue
		}
		if k.Kid == "" {
			// Without kid we can't look up; skip. Tokens without kid are
			// also rejected at the provider level.
			continue
		}
		parsed[k.Kid] = parsedKey{Public: pk, Alg: alg}
	}

	c.keys = parsed
	c.lastSuccessful = c.now()
	c.lastErr = nil
	// Clear the unknown-kid grace window on a successful refresh so the
	// next miss can re-attempt a refresh immediately (the prior miss may
	// have been for a kid that's now present in the freshly-loaded JWKS).
	c.lastMissRefresh = time.Time{}
	return nil
}

// parseJWK extracts the public key material and derives the signing
// algorithm from the JWK. The returned alg is the value the provider
// will require the token header to match.
func parseJWK(k jwk) (any, string, error) {
	switch k.Kty {
	case "RSA":
		pk, err := parseRSAPublicKey(k)
		if err != nil {
			return nil, "", err
		}
		alg := k.Alg
		if alg == "" {
			alg = "RS256" // RFC 7518 default for RSA when alg is absent
		}
		return pk, alg, nil
	case "EC":
		pk, alg, err := parseECDSAPublicKey(k)
		if err != nil {
			return nil, "", err
		}
		return pk, alg, nil
	default:
		return nil, "", fmt.Errorf("unsupported kty %q", k.Kty)
	}
}

func parseRSAPublicKey(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode RSA n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode RSA e: %w", err)
	}
	// e is a big-endian variable-length integer.
	var e int
	switch len(eBytes) {
	case 0:
		return nil, fmt.Errorf("empty RSA exponent")
	case 1:
		e = int(eBytes[0])
	case 2:
		e = int(binary.BigEndian.Uint16(eBytes))
	case 3:
		padded := make([]byte, 4)
		copy(padded[1:], eBytes)
		e = int(binary.BigEndian.Uint32(padded))
	case 4:
		e = int(binary.BigEndian.Uint32(eBytes))
	default:
		return nil, fmt.Errorf("RSA exponent too large: %d bytes", len(eBytes))
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: e,
	}, nil
}

func parseECDSAPublicKey(k jwk) (*ecdsa.PublicKey, string, error) {
	var (
		curve elliptic.Curve
		alg   string
	)
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
		alg = "ES256"
	case "P-384":
		curve = elliptic.P384()
		alg = "ES384"
	case "P-521":
		curve = elliptic.P521()
		alg = "ES512"
	default:
		return nil, "", fmt.Errorf("unsupported curve %q", k.Crv)
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, "", fmt.Errorf("decode EC x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, "", fmt.Errorf("decode EC y: %w", err)
	}
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, alg, nil
}

// allowedAlgs lists the asymmetric signing algorithms accepted by the
// provider. Symmetric (HMAC) and "none" are NEVER accepted, even if a
// JWKS advertises them.
var allowedAlgs = map[string]struct{}{
	"RS256": {}, "RS384": {}, "RS512": {},
	"PS256": {}, "PS384": {}, "PS512": {},
	"ES256": {}, "ES384": {}, "ES512": {},
}

func isAllowedAlg(alg string) bool {
	_, ok := allowedAlgs[alg]
	return ok
}

// allowedAlgNames returns the algorithm names in a slice — for passing to
// jwt.WithValidMethods.
func allowedAlgNames() []string {
	out := make([]string, 0, len(allowedAlgs))
	for a := range allowedAlgs {
		out = append(out, a)
	}
	return out
}
