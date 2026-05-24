// Package aws_sigv4 authenticates AWS-IAM callers using the pre-signed
// URL pattern. Same approach as aws-iam-authenticator (the EKS auth bridge):
// the caller uses their AWS SDK to compute a pre-signed STS
// GetCallerIdentity URL, wraps it as a Bearer token, and sends it to
// Forge. Forge invokes the URL on STS, which validates the signature
// (it was signed for STS's host) and returns the canonical
// ARN / Account / UserID. Forge stamps an Identity from that response.
//
// Forge never possesses the caller's AWS secret key. The cryptographic
// work happens once on the caller side via standard SDK calls.
//
// # Client-side contract (3 lines)
//
//	# Python / boto3
//	import boto3, base64
//	url   = boto3.client('sts', region_name='us-east-1').generate_presigned_url(
//	            'get_caller_identity', ExpiresIn=900)
//	token = 'forge-aws-v1.' + base64.urlsafe_b64encode(url.encode()).rstrip(b'=').decode()
//	requests.post(forge_url, headers={'Authorization': f'Bearer {token}'}, data=msg)
//
// Reference client in scripts/forge-aws-sign.py.
//
// # Wire format
//
//	Authorization: Bearer forge-aws-v1.<base64url-of-presigned-sts-url>
//
// The base64-decoded payload is a complete pre-signed URL of the form:
//
//	https://sts.<region>.amazonaws.com/
//	  ?Action=GetCallerIdentity
//	  &Version=2011-06-15
//	  &X-Amz-Algorithm=AWS4-HMAC-SHA256
//	  &X-Amz-Credential=<AKID>/<YYYYMMDD>/<region>/sts/aws4_request
//	  &X-Amz-Date=<YYYYMMDDTHHMMSSZ>
//	  &X-Amz-Expires=<seconds, max 900>
//	  &X-Amz-SignedHeaders=host
//	  &X-Amz-Signature=<hex>
//
// # SSRF guard
//
// Before invoking the URL, Forge validates the host matches
// sts.<configured-region>.amazonaws.com exactly. A token whose URL
// points anywhere else is rejected — the token must not be usable to
// coerce Forge into calling an arbitrary internal endpoint.
//
// # Caching
//
// Verified identities are cached for IdentityCacheTTL keyed on
// hash(AKID, YYYYMMDD), extracted from the token's X-Amz-Credential.
// Rotating AKID or rolling past midnight UTC invalidates the bucket.
// Errors are never cached.
//
// # Decisions
//
// §9.1 — no aws-sdk-go-v2 dependency. The STS RPC is hand-rolled HTTP +
// XML; trade-off is smaller attack surface and no transitive deps.
//
// §9.3 — allowed_principals are shell-style globs (path.Match).
//
// # Audit reason codes (Phase 1 contract)
//
//	rejected             — STS 4xx (expired/bad sig), ARN allowlist miss,
//	                       URL host mismatch, region scope mismatch
//	provider_unavailable — STS 5xx, network failure, parse failure
//	invalid              — token format malformed, base64 fails, URL fails,
//	                       missing required query params
//	not_for_me           — token didn't start with "forge-aws-v1."
package aws_sigv4

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// ProviderName is the type name used to register and reference this provider.
const ProviderName = "aws_sigv4"

// Defaults.
const (
	defaultIdentityCacheTTL = 60 * time.Second
	defaultHTTPTimeout      = 5 * time.Second
)

// Config controls the aws_sigv4 provider.
type Config struct {
	// Region is the AWS region whose STS endpoint validates signatures.
	// REQUIRED. The pre-signed URL's host MUST match
	// sts.<region>.amazonaws.com exactly.
	Region string `yaml:"region"`

	// Audience is informational only — emitted in the audit log's Claims
	// payload. STS itself doesn't enforce it.
	Audience string `yaml:"audience,omitempty"`

	// AllowedPrincipals is an optional list of shell-style globs (§9.3)
	// matched against the STS-returned ARN. Empty list means "allow any
	// IAM principal that has a valid AWS key" — fine for single-tenant
	// dev, never appropriate for production.
	//
	// Patterns match the STS assumed-role ARN form
	// ("arn:aws:sts::ACCOUNT:assumed-role/RoleName/SessionName"), NOT the
	// IAM role ARN ("arn:aws:iam::ACCOUNT:role/RoleName").
	AllowedPrincipals []string `yaml:"allowed_principals,omitempty"`

	// IdentityCacheTTL bounds how long a verified Identity is reused
	// without re-checking with STS. Defaults to 60s.
	IdentityCacheTTL time.Duration `yaml:"identity_cache_ttl,omitempty"`

	// STSEndpoint is a TEST-ONLY override that changes the expected
	// pre-signed URL host (and relaxes the https requirement). Production
	// should leave this empty.
	STSEndpoint string `yaml:"sts_endpoint,omitempty"`

	// HTTPTimeout caps each STS call. Defaults to 5s.
	HTTPTimeout time.Duration `yaml:"http_timeout,omitempty"`
}

// Validate returns ErrProviderNotConfigured when required fields are missing.
func (c Config) Validate() error {
	if c.Region == "" {
		return fmt.Errorf("%w: region required", auth.ErrProviderNotConfigured)
	}
	return nil
}

// Provider implements auth.Provider for AWS-IAM callers.
type Provider struct {
	cfg          Config
	expectedHost string // computed once: sts.<region>.amazonaws.com or test override
	requireHTTPS bool   // false only when STSEndpoint test override is in use
	cache        *IdentityCache
	sts          *STSClient
	matcher      *ArnMatcher
}

// New constructs a Provider after validating cfg.
func New(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.IdentityCacheTTL == 0 {
		cfg.IdentityCacheTTL = defaultIdentityCacheTTL
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	matcher, err := NewArnMatcher(cfg.AllowedPrincipals)
	if err != nil {
		return nil, fmt.Errorf("aws_sigv4: allowed_principals: %w", err)
	}

	expectedHost := fmt.Sprintf("sts.%s.amazonaws.com", cfg.Region)
	requireHTTPS := true
	if cfg.STSEndpoint != "" {
		h, scheme := hostAndSchemeOf(cfg.STSEndpoint)
		if h != "" {
			expectedHost = h
		}
		// Test override: allow plain http for httptest servers.
		if scheme == "http" {
			requireHTTPS = false
		}
	}

	return &Provider{
		cfg:          cfg,
		expectedHost: expectedHost,
		requireHTTPS: requireHTTPS,
		cache:        NewIdentityCache(cfg.IdentityCacheTTL),
		sts:          NewSTSClient(cfg.Region, "", cfg.HTTPTimeout),
		matcher:      matcher,
	}, nil
}

// Name implements auth.Provider.
func (p *Provider) Name() string { return ProviderName }

// Verify implements auth.Provider.
//
// The middleware extracts the Bearer token and passes it here. If the
// token doesn't start with "forge-aws-v1." we yield to the next chain
// entry. Otherwise we validate the embedded pre-signed URL, GET it on
// STS, and stamp an Identity from the returned ARN.
func (p *Provider) Verify(ctx context.Context, token string, _ auth.Headers) (*auth.Identity, error) {
	parsed, err := ParseToken(token, p.expectedHost, p.requireHTTPS)
	if err != nil {
		// Only the prefix check distinguishes "this isn't my token"
		// from "this IS my token but malformed." Use a sentinel so the
		// chain can fall through on the former but stop on the latter.
		if err.Error() == "missing forge-aws-v1 prefix" {
			return nil, auth.ErrTokenNotForMe
		}
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	// Region in the credential scope must match our configured region.
	// Defends against cross-region replay (e.g. a token pre-signed for
	// us-east-1 hitting a Forge instance configured for eu-west-1).
	if parsed.Region != p.cfg.Region {
		return nil, fmt.Errorf("%w: token region %q != configured %s", auth.ErrTokenRejected, parsed.Region, p.cfg.Region)
	}

	cacheKey := dateBucketKey(parsed.AKID, parsed.Date)
	if id, ok := p.cache.Get(cacheKey); ok {
		return id, nil
	}

	caller, err := p.sts.GetCallerIdentity(ctx, parsed.URL.String())
	if err != nil {
		return nil, err // STSClient already wraps with the right sentinel
	}

	if !p.matcher.Match(caller.Arn) {
		return nil, fmt.Errorf("%w: ARN %q not in allowed_principals", auth.ErrTokenRejected, caller.Arn)
	}

	id := &auth.Identity{
		UserID: caller.Arn,
		OrgID:  caller.Account,
		Source: ProviderName,
		Claims: map[string]any{
			"user_id":  caller.UserID,
			"arn":      caller.Arn,
			"account":  caller.Account,
			"audience": p.cfg.Audience,
		},
	}
	p.cache.Put(cacheKey, id)
	return id, nil
}

// dateBucketKey hashes (AKID, YYYYMMDD) so two requests from the same
// AKID on the same day collapse to a single STS call per
// IdentityCacheTTL window. Hashing protects against length-leak / log-scan
// reads of the cache key.
func dateBucketKey(akid, date string) string {
	bucket := date
	if len(bucket) > 8 {
		bucket = bucket[:8] // YYYYMMDD
	}
	sum := sha256.Sum256([]byte(akid + "|" + bucket))
	return hex.EncodeToString(sum[:])
}

// hostAndSchemeOf is a forgiving parser used only at Factory time for the
// STSEndpoint test override. Returns (host, scheme) or empty strings on
// parse failure.
func hostAndSchemeOf(raw string) (host, scheme string) {
	// Accept both bare "host:port" and full "scheme://host:port/path" forms.
	// For the test override we only care about the host portion (for
	// matching) and whether scheme is http (so we can relax the https check).
	if i := indexOf(raw, "://"); i >= 0 {
		scheme = raw[:i]
		rest := raw[i+3:]
		for k := 0; k < len(rest); k++ {
			if rest[k] == '/' {
				return rest[:k], scheme
			}
		}
		return rest, scheme
	}
	// No scheme: assume host:port form
	for k := 0; k < len(raw); k++ {
		if raw[k] == '/' {
			return raw[:k], ""
		}
	}
	return raw, ""
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func init() {
	auth.Register(ProviderName, func(settings map[string]any) (auth.Provider, error) {
		var cfg Config
		if err := auth.UnmarshalSettings(settings, &cfg); err != nil {
			return nil, err
		}
		return New(cfg)
	})
}
