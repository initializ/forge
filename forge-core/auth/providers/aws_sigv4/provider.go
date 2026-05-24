// Package aws_sigv4 authenticates requests signed with AWS Sigv4 by
// reflecting the signature to AWS STS GetCallerIdentity. Same pattern as
// aws-iam-authenticator (the EKS auth bridge).
//
// Forge never has the caller's secret key — STS validates the signature
// on Forge's behalf and returns the canonical ARN/Account/UserID. Verified
// identities are cached by hash(AKID|YYYYMMDD) for IdentityCacheTTL so the
// hot path doesn't bounce through STS on every call.
//
// # Client-side signing contract (READ THIS BEFORE INTEGRATING)
//
// Sigv4 binds the signature to the destination host as part of its canonical
// headers. Forge does not — and cannot — relax that. As a consequence,
// callers MUST sign their request as if they were going to STS directly,
// then attach the resulting headers to a request that's sent to Forge:
//
//  1. Construct an STS request shape (POST https://sts.<region>.amazonaws.com/,
//     body Action=GetCallerIdentity&Version=2011-06-15) — but do not send it.
//  2. Sign that hypothetical request with the caller's AWS credentials, using
//     service=sts and region=<region>.
//  3. Take the resulting Authorization + X-Amz-Date (+ X-Amz-Security-Token
//     for temporary credentials, e.g. SSO / IRSA / Lambda) headers and attach
//     them to the REAL POST that goes to Forge's /tasks/send endpoint.
//  4. Forge will forward those headers verbatim to STS; STS will validate
//     them against its own host and return the caller's canonical ARN.
//
// Standard tools (awscurl, the AWS CLI, boto3.client('sts')) do NOT do this
// automatically — they always sign for the URL they are addressing. A small
// wrapper is required. See scripts/forge-aws-sign.py for the canonical
// reference implementation in ~30 lines of boto3.
//
// Decision §9.1: no aws-sdk-go-v2 dependency — the STS RPC is ~150 LOC of
// hand-rolled HTTP + XML. Trade-off: small attack surface, predictable
// behavior, no transitive deps; cost: we maintain the RPC ourselves.
//
// Decision §9.3: allowed_principals are shell-style globs (path.Match).
//
// Audit reason codes (Phase 1 contract):
//
//	rejected             — scope check, ARN allowlist miss, STS 4xx
//	provider_unavailable — STS 5xx, network failure, parse failure
//	invalid              — Sigv4 header malformed
//	not_for_me           — Authorization header didn't start with AWS4-HMAC-SHA256
package aws_sigv4

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
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
	// REQUIRED. Defense in depth: callers must sign for this exact region
	// (Sigv4 cross-region replay rejected at parse-time scope check).
	Region string `yaml:"region"`

	// Audience is informational only — emitted in audit logs for context.
	// STS itself doesn't enforce this.
	Audience string `yaml:"audience,omitempty"`

	// AllowedPrincipals is an optional list of shell-style globs (decision
	// §9.3) matched against the STS-returned ARN. Empty list means "allow
	// any IAM principal in the account that signed the request" — fine
	// for single-tenant dev setups, never appropriate for prod.
	//
	// Patterns match the assumed-role ARN form
	// ("arn:aws:sts::ACCOUNT:assumed-role/RoleName/SessionName"), NOT the
	// role's own ARN ("arn:aws:iam::ACCOUNT:role/RoleName"). PR6 docs
	// call this out explicitly with a worked example.
	AllowedPrincipals []string `yaml:"allowed_principals,omitempty"`

	// IdentityCacheTTL bounds how long a verified Identity is reused
	// without re-checking with STS. Defaults to 60s. Sets the upper
	// bound on the window in which a revoked AKID remains accepted.
	IdentityCacheTTL time.Duration `yaml:"identity_cache_ttl,omitempty"`

	// STSEndpoint is a TEST-ONLY override that points STS calls at an
	// alternate URL. Production should leave this empty so the region
	// derives the canonical sts.<region>.amazonaws.com host.
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

// Provider implements auth.Provider for AWS Sigv4 callers.
type Provider struct {
	cfg     Config
	parser  Parser
	cache   *IdentityCache
	sts     *STSClient
	matcher *ArnMatcher
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
	return &Provider{
		cfg:     cfg,
		parser:  Parser{},
		cache:   NewIdentityCache(cfg.IdentityCacheTTL),
		sts:     NewSTSClient(cfg.Region, cfg.STSEndpoint, cfg.HTTPTimeout),
		matcher: matcher,
	}, nil
}

// Name implements auth.Provider.
func (p *Provider) Name() string { return ProviderName }

// Verify implements auth.Provider.
//
// Token is unused — Sigv4 carries everything it needs in the Authorization
// header (and X-Amz-Date, X-Amz-Security-Token), which the middleware
// hands us via the headers map.
func (p *Provider) Verify(ctx context.Context, _ string, headers auth.Headers) (*auth.Identity, error) {
	authHeader := headers.Get("Authorization")
	if !strings.HasPrefix(authHeader, sigv4Algorithm+" ") {
		return nil, auth.ErrTokenNotForMe
	}

	sig, err := p.parser.Parse(authHeader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	// Scope check (defense in depth §3.1): reject signatures meant for a
	// different AWS service or region. Prevents cross-service Sigv4
	// replay (e.g. a request signed for S3 reaching us configured for STS).
	if sig.Service != "sts" {
		return nil, fmt.Errorf("%w: signature scoped to service %q, expected sts", auth.ErrTokenRejected, sig.Service)
	}
	if sig.Region != p.cfg.Region {
		return nil, fmt.Errorf("%w: signature scoped to region %q, expected %s", auth.ErrTokenRejected, sig.Region, p.cfg.Region)
	}

	cacheKey := dateBucketKey(sig.AKID, sig.Date)
	if id, ok := p.cache.Get(cacheKey); ok {
		return id, nil
	}

	caller, err := p.sts.GetCallerIdentity(ctx, STSReflectArgs{
		AuthHeader:    authHeader,
		AmzDate:       headers.Get("X-Amz-Date"),
		SecurityToken: headers.Get("X-Amz-Security-Token"),
	})
	if err != nil {
		// STSClient already wraps with the right sentinel
		// (ErrTokenRejected for 4xx, ErrProviderUnavailable for 5xx/network).
		return nil, err
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

// dateBucketKey hashes (AKID, YYYYMMDD) so two requests from the same AKID
// on the same day collapse to one STS call per IdentityCacheTTL window.
// Hashing protects the cache against length-leak / log-scan attacks on the
// raw key — same reasoning as static_token (review #11).
func dateBucketKey(akid, sigv4Date string) string {
	bucket := sigv4Date
	if len(bucket) > 8 {
		bucket = bucket[:8] // YYYYMMDD
	}
	sum := sha256.Sum256([]byte(akid + "|" + bucket))
	return hex.EncodeToString(sum[:])
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
