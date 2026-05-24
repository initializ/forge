// Package azure_ad authenticates Microsoft Entra ID (Azure AD) tokens.
// Composes the Phase 1 oidc.Provider (decision §9.2) for the heavy
// lifting — signature verify and base claim validation — and layers
// AAD-specific concerns on top:
//
//   - Tenant lock-in via the `tid` claim
//   - Optional Microsoft Graph group enrichment when the JWT's groups
//     claim overflows (AAD truncates at ~200 groups)
//   - Correct issuer template for single- vs. multi-tenant
//
// Decision §9.5: standard Bearer flow; no widened-header use.
//
// Audit reason codes (Phase 1 contract):
//
//	rejected             — bad signature, expired, tid mismatch,
//	                       aud mismatch, Graph 401/403
//	invalid              — missing tid, malformed claims,
//	                       unsupported alg
//	provider_unavailable — AAD JWKS down, Graph 5xx
//	not_for_me           — empty Bearer (delegates to OIDC's looksLikeJWT)
package azure_ad

import (
	"context"
	"fmt"
	"time"

	"github.com/initializ/forge/forge-core/auth"
	"github.com/initializ/forge/forge-core/auth/providers/oidc"
)

// ProviderName is the registry name.
const ProviderName = "azure_ad"

const (
	aadAuthorityBase = "https://login.microsoftonline.com"

	defaultGraphTimeout  = 5 * time.Second
	defaultJWKSCacheTTL  = time.Hour
	defaultGraphCacheTTL = 5 * time.Minute
)

// Config controls the azure_ad provider.
type Config struct {
	// TenantID is the Entra tenant GUID. REQUIRED unless AllowMultiTenant
	// is true.
	TenantID string `yaml:"tenant_id"`

	// Audience is REQUIRED. Typically the Application ID URI from the
	// app registration (e.g. "api://forge").
	Audience string `yaml:"audience"`

	// AllowMultiTenant enables accepting tokens from ANY Entra tenant.
	// Defaults to false (single-tenant — safe choice). PR6 docs flag the
	// security implications of flipping this on in production.
	AllowMultiTenant bool `yaml:"allow_multi_tenant,omitempty"`

	// GroupsMode is "claim" (default — uses the in-JWT groups/roles
	// claim) or "graph" (queries Microsoft Graph when groups are missing,
	// i.e. AAD overage).
	GroupsMode string `yaml:"groups_mode,omitempty"`

	// GraphTimeout caps each Graph call. Default 5s. Only used when
	// GroupsMode == "graph".
	GraphTimeout time.Duration `yaml:"graph_timeout,omitempty"`

	// JWKSCacheTTL bounds the JWKS cache age. Defaults to 1h.
	JWKSCacheTTL time.Duration `yaml:"jwks_cache_ttl,omitempty"`

	// GraphEndpoint is a TEST-ONLY override pointing at a fake Graph
	// server. Empty in production.
	GraphEndpoint string `yaml:"-"`
}

// Validate returns ErrProviderNotConfigured when required fields are missing.
func (c Config) Validate() error {
	if c.Audience == "" {
		return fmt.Errorf("%w: audience required (e.g. api://forge)", auth.ErrProviderNotConfigured)
	}
	if !c.AllowMultiTenant && c.TenantID == "" {
		return fmt.Errorf("%w: tenant_id required unless allow_multi_tenant=true", auth.ErrProviderNotConfigured)
	}
	if c.GroupsMode != "" && c.GroupsMode != "claim" && c.GroupsMode != "graph" {
		return fmt.Errorf("%w: groups_mode must be 'claim' or 'graph', got %q", auth.ErrProviderNotConfigured, c.GroupsMode)
	}
	return nil
}

// ExtractTenantID returns the "tid" claim, or "" if it's missing /
// non-string. The empty-return form lets callers distinguish "missing"
// from "wrong tenant" without a typed error.
func ExtractTenantID(claims map[string]any) string {
	tid, _ := claims["tid"].(string)
	return tid
}

// Provider implements auth.Provider for AAD callers.
type Provider struct {
	cfg   Config
	oidc  *oidc.Provider // composition (decision §9.2)
	graph *GraphClient   // nil unless GroupsMode == "graph"
	cache *GraphCache    // nil unless GroupsMode == "graph"
}

// New constructs a Provider after validating cfg.
func New(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.GroupsMode == "" {
		cfg.GroupsMode = "claim"
	}
	if cfg.GraphTimeout == 0 {
		cfg.GraphTimeout = defaultGraphTimeout
	}
	if cfg.JWKSCacheTTL == 0 {
		cfg.JWKSCacheTTL = defaultJWKSCacheTTL
	}

	inner, err := oidc.New(oidc.Config{
		Issuer:          resolveIssuer(cfg),
		Audience:        cfg.Audience,
		JWKSCacheTTL:    cfg.JWKSCacheTTL,
		SkipIssuerCheck: cfg.AllowMultiTenant,
	})
	if err != nil {
		return nil, fmt.Errorf("azure_ad: composing oidc provider: %w", err)
	}

	p := &Provider{cfg: cfg, oidc: inner}
	if cfg.GroupsMode == "graph" {
		if cfg.GraphEndpoint != "" {
			p.graph = NewGraphClientWithEndpoint(cfg.GraphEndpoint, cfg.GraphTimeout)
		} else {
			p.graph = NewGraphClient(cfg.GraphTimeout)
		}
		p.cache = NewGraphCache(defaultGraphCacheTTL)
	}
	return p, nil
}

// Name implements auth.Provider.
func (p *Provider) Name() string { return ProviderName }

// Verify implements auth.Provider.
func (p *Provider) Verify(ctx context.Context, token string, headers auth.Headers) (*auth.Identity, error) {
	id, err := p.oidc.Verify(ctx, token, headers)
	if err != nil {
		return nil, err
	}

	// Tenant gate (single-tenant default).
	if !p.cfg.AllowMultiTenant {
		tid := ExtractTenantID(id.Claims)
		if tid == "" {
			return nil, fmt.Errorf("%w: AAD token missing tid claim", auth.ErrInvalidToken)
		}
		if tid != p.cfg.TenantID {
			return nil, fmt.Errorf("%w: tid mismatch", auth.ErrTokenRejected)
		}
	}

	// Optional Graph enrichment.
	if p.cfg.GroupsMode == "graph" && needsEnrichment(id.Groups) {
		if enriched, err := p.enrichGroups(ctx, id, headers); err == nil {
			id.Groups = enriched
		}
		// Soft-fail on Graph failure: leave Groups empty rather than
		// blocking prod traffic on a transient outage. Hard-fail mode
		// (graph_required: true) is out of scope for v0.11.
	}

	id.Source = ProviderName // overwrite oidc's "oidc" stamp
	return id, nil
}

// resolveIssuer picks the issuer URL passed to the composed OIDC
// provider. For single-tenant, that's the full per-tenant authority.
// For multi-tenant, the "common" endpoint serves JWKS for all tenants
// — we point OIDC there and disable iss equality (azure_ad enforces
// tenant via the tid claim instead).
func resolveIssuer(cfg Config) string {
	if cfg.AllowMultiTenant {
		return aadAuthorityBase + "/common/v2.0"
	}
	return fmt.Sprintf("%s/%s/v2.0", aadAuthorityBase, cfg.TenantID)
}

// needsEnrichment returns true when Graph should be consulted. AAD
// emits no `groups` claim (or a `_claim_names` indicator) when a user
// is in too many groups. Phase 1 OIDC surfaces empty Groups in that
// case — treating empty as "enrich" catches it.
func needsEnrichment(groups []string) bool {
	return len(groups) == 0
}

func (p *Provider) enrichGroups(ctx context.Context, id *auth.Identity, headers auth.Headers) ([]string, error) {
	if cached, ok := p.cache.Get(id.UserID); ok {
		return cached, nil
	}
	groups, err := p.graph.TransitiveMemberOf(ctx, id.UserID, headers.Get("Authorization"))
	if err != nil {
		return nil, err
	}
	p.cache.Put(id.UserID, groups)
	return groups, nil
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
