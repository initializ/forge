package oidc_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeIssuer is a minimal in-memory OIDC issuer: serves the discovery
// document and JWKS, and helps tests sign tokens with the matching key.
type fakeIssuer struct {
	t      *testing.T
	server *httptest.Server
	// keys keyed by kid → signer
	keys map[string]*signingKey
	// keyOrder remembers insertion order so JWKS responses are stable.
	keyOrder []string
	// jwksFetches counts how many times /jwks was hit (race-safe via the
	// server's serialized handler).
	jwksFetches int
}

type signingKey struct {
	kid    string
	method jwt.SigningMethod
	priv   any // *rsa.PrivateKey or *ecdsa.PrivateKey
	pub    any // *rsa.PublicKey or *ecdsa.PublicKey
	// alg is what the JWKS will declare; mismatch with `method` is what
	// algorithm-confusion tests need.
	algInJWKS string
}

// newFakeIssuer builds a default RSA-keyed issuer with kid "key-1".
func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	fi := &fakeIssuer{
		t:    t,
		keys: map[string]*signingKey{},
	}
	fi.addRSAKey("key-1")
	fi.server = httptest.NewServer(http.HandlerFunc(fi.serve))
	t.Cleanup(fi.server.Close)
	return fi
}

// IssuerURL is the canonical issuer URL (no trailing slash).
func (fi *fakeIssuer) IssuerURL() string { return fi.server.URL }

// addRSAKey generates a fresh RSA key and registers it with the given kid.
func (fi *fakeIssuer) addRSAKey(kid string) *signingKey {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		fi.t.Fatalf("generate RSA key: %v", err)
	}
	k := &signingKey{
		kid:       kid,
		method:    jwt.SigningMethodRS256,
		priv:      priv,
		pub:       &priv.PublicKey,
		algInJWKS: "RS256",
	}
	fi.keys[kid] = k
	fi.keyOrder = append(fi.keyOrder, kid)
	return k
}

// addECDSAKey generates a fresh P-256 ECDSA key.
func (fi *fakeIssuer) addECDSAKey(kid string) *signingKey {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fi.t.Fatalf("generate ECDSA key: %v", err)
	}
	k := &signingKey{
		kid:       kid,
		method:    jwt.SigningMethodES256,
		priv:      priv,
		pub:       &priv.PublicKey,
		algInJWKS: "ES256",
	}
	fi.keys[kid] = k
	fi.keyOrder = append(fi.keyOrder, kid)
	return k
}

// SignWith builds a signed token using the named key. Claims is the full
// claim map; helpers below build common claim sets.
func (fi *fakeIssuer) SignWith(kid string, claims jwt.MapClaims) string {
	fi.t.Helper()
	k, ok := fi.keys[kid]
	if !ok {
		fi.t.Fatalf("unknown signing key %q", kid)
	}
	tok := jwt.NewWithClaims(k.method, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(k.priv)
	if err != nil {
		fi.t.Fatalf("sign: %v", err)
	}
	return s
}

// SignUnsigned returns a token with `alg: none` and no signature segment.
// jwt/v5 doesn't expose a clean way to construct these; we do it manually.
func SignUnsigned(claims jwt.MapClaims) string {
	header := map[string]any{"alg": "none", "typ": "JWT", "kid": "none-kid"}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc(hb) + "." + enc(cb) + "."
}

// SignHMAC returns a token signed with HS256 — must be rejected by the
// provider since HMAC + JWKS is a footgun (anyone with the JWKS could
// produce one).
func SignHMAC(kid string, claims jwt.MapClaims, secret []byte) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = kid
	s, _ := tok.SignedString(secret)
	return s
}

// DefaultClaims returns a claim set that satisfies the default fakeIssuer
// + a single audience.
func (fi *fakeIssuer) DefaultClaims(audience string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": fi.IssuerURL(),
		"aud": audience,
		"sub": "user-123",
		"iat": now.Unix(),
		"exp": now.Add(15 * time.Minute).Unix(),
	}
}

// serve handles both the discovery endpoint and the JWKS endpoint.
func (fi *fakeIssuer) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   fi.IssuerURL(),
			"jwks_uri": fi.IssuerURL() + "/jwks",
		})
	case "/jwks":
		fi.jwksFetches++
		_ = json.NewEncoder(w).Encode(fi.jwksDoc())
	default:
		http.NotFound(w, r)
	}
}

// jwksDoc constructs the JWKS document from the current key set.
func (fi *fakeIssuer) jwksDoc() map[string]any {
	keys := make([]map[string]any, 0, len(fi.keyOrder))
	for _, kid := range fi.keyOrder {
		k := fi.keys[kid]
		switch pub := k.pub.(type) {
		case *rsa.PublicKey:
			keys = append(keys, map[string]any{
				"kty": "RSA",
				"kid": kid,
				"use": "sig",
				"alg": k.algInJWKS,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(intToBigEndian(pub.E)),
			})
		case *ecdsa.PublicKey:
			keys = append(keys, map[string]any{
				"kty": "EC",
				"kid": kid,
				"use": "sig",
				"alg": k.algInJWKS,
				"crv": ecCurveName(pub.Curve),
				"x":   base64.RawURLEncoding.EncodeToString(pub.X.Bytes()),
				"y":   base64.RawURLEncoding.EncodeToString(pub.Y.Bytes()),
			})
		default:
			panic(fmt.Sprintf("unsupported key type: %T", pub))
		}
	}
	return map[string]any{"keys": keys}
}

func intToBigEndian(e int) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(e))
	// Strip leading zero bytes — RFC 7518 demands no leading zeros.
	for len(buf) > 1 && buf[0] == 0 {
		buf = buf[1:]
	}
	return buf
}

func ecCurveName(c elliptic.Curve) string {
	switch c {
	case elliptic.P256():
		return "P-256"
	case elliptic.P384():
		return "P-384"
	case elliptic.P521():
		return "P-521"
	default:
		return ""
	}
}

// Compile-time assertion: tests reference math/big to ensure the import
// graph is exercised in case future refactors lose the dep.
var _ = big.NewInt
