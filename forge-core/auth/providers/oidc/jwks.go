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
//   - First lookup ever: fetch JWKS.
//   - kid not in cache and last fetch is older than the small "refetch
//     grace window" (avoids stampedes on bad input): fetch JWKS again.
//   - kid still not in cache after refresh: return ErrKeyNotFound.
//   - kid in cache: return immediately.
//
// The cache does not eagerly expire entries by TTL — keys are durable; we
// refresh on miss. This matches how real-world identity providers rotate
// JWKS (add new key, then later remove old key — the new kid triggers a
// refresh that picks up both).
type jwksCache struct {
	jwksURL    func(ctx context.Context) (string, error)
	httpClient *http.Client
	ttl        time.Duration
	// refetchGrace prevents a malformed kid from triggering one JWKS fetch
	// per request. Within this window, repeated unknown-kid lookups skip
	// the refetch.
	refetchGrace time.Duration

	mu        sync.RWMutex
	keys      map[string]parsedKey
	lastFetch time.Time
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
	if ttl < minJWKSTTL {
		ttl = minJWKSTTL
	}
	return &jwksCache{
		jwksURL:      jwksURL,
		httpClient:   client,
		ttl:          ttl,
		refetchGrace: defaultRefetchGrace,
		keys:         map[string]parsedKey{},
	}
}

// ErrKeyNotFound is returned when a `kid` is not in the cache and a
// refresh did not produce it.
var ErrKeyNotFound = fmt.Errorf("oidc: signing key not found")

// Get returns the parsed key for the given kid, refreshing the JWKS cache
// if necessary. Safe for concurrent use.
//
// Refresh semantics:
//   - kid in cache → return immediately.
//   - kid missing, never refreshed for a miss before → refresh once.
//   - kid missing AND we already refreshed for a miss recently (within
//     refetchGrace) → deny without re-fetching, to prevent a stream of
//     bad kids from hammering the JWKS endpoint.
func (c *jwksCache) Get(ctx context.Context, kid string) (parsedKey, error) {
	// Fast path: lock-free read.
	c.mu.RLock()
	if key, ok := c.keys[kid]; ok {
		c.mu.RUnlock()
		return key, nil
	}
	cooldownActive := !c.lastMissRefresh.IsZero() && time.Since(c.lastMissRefresh) < c.refetchGrace
	c.mu.RUnlock()

	if cooldownActive {
		return parsedKey{}, ErrKeyNotFound
	}

	// Miss — refresh. Take the write lock first so concurrent misses
	// coalesce into one fetch.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check after acquiring the write lock — another goroutine may
	// have just refreshed and populated this kid.
	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	if !c.lastMissRefresh.IsZero() && time.Since(c.lastMissRefresh) < c.refetchGrace {
		return parsedKey{}, ErrKeyNotFound
	}

	if err := c.refreshLocked(ctx); err != nil {
		return parsedKey{}, err
	}
	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	// Refresh happened but kid is still missing — remember that so we
	// don't hammer JWKS on the next unknown-kid request.
	c.lastMissRefresh = time.Now()
	return parsedKey{}, ErrKeyNotFound
}

// refreshLocked fetches the JWKS endpoint and replaces the cache. Must be
// called with c.mu held for writing.
func (c *jwksCache) refreshLocked(ctx context.Context) error {
	url, err := c.jwksURL(ctx)
	if err != nil {
		c.lastErr = err
		c.lastFetch = time.Now()
		return fmt.Errorf("oidc: resolve jwks_uri: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.lastErr = err
		c.lastFetch = time.Now()
		return fmt.Errorf("oidc: build jwks request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.lastErr = err
		c.lastFetch = time.Now()
		return fmt.Errorf("oidc: jwks fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		err := fmt.Errorf("oidc: jwks returned status %d", resp.StatusCode)
		c.lastErr = err
		c.lastFetch = time.Now()
		return err
	}

	var body struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		c.lastErr = err
		c.lastFetch = time.Now()
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
	c.lastFetch = time.Now()
	c.lastErr = nil
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
