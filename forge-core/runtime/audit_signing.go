package runtime

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

// AuditSigner mints Ed25519 signatures over the canonical JSON of an
// AuditEvent. Nil signer is a valid state — signing is opt-in (an
// operator who hasn't wired a key gets the pre-#213 wire shape).
//
// Loading key material is separated from signing so the same key
// source can be used for the JWKS endpoint's public-key advertise
// path. See forge-cli/runtime/runner.go for the wiring.
type AuditSigner struct {
	priv ed25519.PrivateKey
	kid  string
}

// LoadedKey pairs a private key with the operator-supplied key id.
// Exported so callers (runner, tests) can pass it around without
// re-parsing.
type LoadedKey struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	Kid     string
}

// NewAuditSigner constructs a signer around a loaded key.
func NewAuditSigner(k LoadedKey) *AuditSigner {
	return &AuditSigner{priv: k.Private, kid: k.Kid}
}

// Kid returns the current key id — surfaced on every signed event
// and on the JWKS pubkey record.
func (s *AuditSigner) Kid() string { return s.kid }

// Sign signs the canonical event bytes and returns a base64
// standard-encoded signature (RFC 4648 §4, matches JWS/JWT
// convention for Ed25519 signatures).
func (s *AuditSigner) Sign(canonical []byte) string {
	sig := ed25519.Sign(s.priv, canonical)
	return base64.StdEncoding.EncodeToString(sig)
}

// LoadEd25519KeyFromEnv reads an Ed25519 private key from the given
// env var (base64-standard-encoded PKCS#8 DER OR PEM). Kid is loaded
// from the paired FORGE_AUDIT_SIGNING_KID env var (falls back to
// "forge-audit-v1" so a single-key deployment doesn't need to set
// two variables).
//
// Returns (nil, nil) when the env var is unset — signing stays off,
// no error. This is intentional: adding the config to a deployment
// enables signing; absence keeps the pre-#213 behavior.
func LoadEd25519KeyFromEnv(varName, kidEnvVar string) (*LoadedKey, error) {
	raw := os.Getenv(varName)
	if raw == "" {
		return nil, nil
	}
	kid := os.Getenv(kidEnvVar)
	if kid == "" {
		kid = "forge-audit-v1"
	}
	priv, err := parseEd25519Private(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", varName, err)
	}
	return &LoadedKey{
		Private: priv,
		Public:  priv.Public().(ed25519.PublicKey),
		Kid:     kid,
	}, nil
}

// LoadEd25519KeyFromFile reads a PKCS#8 PEM Ed25519 key file. Path
// is expanded (~) before opening.
func LoadEd25519KeyFromFile(path, kid string) (*LoadedKey, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path is the intended surface
	if err != nil {
		return nil, fmt.Errorf("reading audit signing key %s: %w", path, err)
	}
	priv, err := parseEd25519PEM(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if kid == "" {
		kid = "forge-audit-v1"
	}
	return &LoadedKey{
		Private: priv,
		Public:  priv.Public().(ed25519.PublicKey),
		Kid:     kid,
	}, nil
}

// parseEd25519Private accepts either base64-encoded PKCS#8 DER OR a
// PEM string. The single accepted algorithm is Ed25519.
func parseEd25519Private(raw string) (ed25519.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	// Try PEM first — the operator may have set the env var to
	// the file contents directly (heredoc / secretRef).
	if strings.HasPrefix(raw, "-----BEGIN") {
		return parseEd25519PEM([]byte(raw))
	}
	// Otherwise treat as base64 PKCS#8 DER.
	derBytes, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("audit signing key is not base64: %w", err)
	}
	return derToEd25519(derBytes)
}

func parseEd25519PEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	return derToEd25519(block.Bytes)
}

func derToEd25519(der []byte) (ed25519.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parsing PKCS#8: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected Ed25519 key, got %T", key)
	}
	return priv, nil
}

// JWKS is the JSON Web Key Set shape served at
// /.well-known/forge-audit-keys. Consumers pull it once at startup
// and cache locally; rotation adds a new entry alongside the old.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWK is one entry — Ed25519 pubkey per RFC 8037.
type JWK struct {
	Kty string `json:"kty"`           // "OKP"
	Crv string `json:"crv"`           // "Ed25519"
	X   string `json:"x"`             // base64url of pubkey (no padding)
	Kid string `json:"kid,omitempty"` // operator-supplied key id
	Use string `json:"use,omitempty"` // "sig"
	Alg string `json:"alg,omitempty"` // "EdDSA"
}

// PublicJWKS produces the JWKS representation of the loaded keys. A
// nil / empty input returns an empty set — safe to serve when
// signing is off.
func PublicJWKS(keys ...LoadedKey) JWKS {
	out := JWKS{Keys: make([]JWK, 0, len(keys))}
	for _, k := range keys {
		out.Keys = append(out.Keys, JWK{
			Kty: "OKP",
			Crv: "Ed25519",
			X:   base64.RawURLEncoding.EncodeToString(k.Public),
			Kid: k.Kid,
			Use: "sig",
			Alg: "EdDSA",
		})
	}
	return out
}

// PublicKeyFromJWK extracts an Ed25519 public key from a JWK. Used
// by the verifier when a pubkey file is supplied on the command line.
func PublicKeyFromJWK(j JWK) (ed25519.PublicKey, error) {
	if j.Kty != "OKP" || j.Crv != "Ed25519" {
		return nil, fmt.Errorf("unsupported JWK: kty=%q crv=%q (want OKP/Ed25519)", j.Kty, j.Crv)
	}
	raw, err := base64.RawURLEncoding.DecodeString(j.X)
	if err != nil {
		return nil, fmt.Errorf("JWK x is not base64url: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("Ed25519 pubkey has wrong length %d, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// VerifySignature checks the base64 signature over `canonical` bytes
// with the given pubkey. Returns nil on match, non-nil otherwise.
func VerifySignature(pub ed25519.PublicKey, canonical []byte, sigB64 string) error {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("signature is not base64: %w", err)
	}
	if !ed25519.Verify(pub, canonical, sig) {
		return errors.New("signature verify failed")
	}
	return nil
}

// canonicalBytesForSigning returns the JSON bytes over which the
// signature is computed. Rule: marshal the event with its Sig field
// EMPTY so the signature covers every other field on the event.
// Since Sig is `omitempty`, an empty value
// produces the same bytes as if the field weren't there — the
// verifier can round-trip through the same transformation.
func canonicalBytesForSigning(evt AuditEvent) ([]byte, error) {
	// Clone-and-blank the Sig field without touching the input.
	toSign := evt
	toSign.Sig = ""
	return json.Marshal(toSign)
}
