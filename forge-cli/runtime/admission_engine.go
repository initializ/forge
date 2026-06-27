package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Baked defaults for the admission HTTP call. Per issue #201 these are
// constants in the binary, not env-overridable: changing them across
// deployments would break the "operators don't have to think about it"
// promise the feature is built on.
const (
	// admissionHTTPTimeout bounds each per-request platform call.
	// Short enough not to drag inbound A2A latency; long enough to
	// absorb DNS jitter, TLS handshake, and a cross-region call on a
	// healthy platform.
	admissionHTTPTimeout = 2 * time.Second

	// admissionCacheTTL is how long Forge caches the latest decision
	// per agent before issuing another platform call. At steady state
	// this means one platform call per agent per 5s — overrun bound
	// is 5s × steady-state RPS × per-invocation cost. The fallback-
	// admit path uses the SAME TTL so a platform outage produces
	// one call per agent per 5s, not one per inbound request.
	admissionCacheTTL = 5 * time.Second
)

// admissionResponse is the JSON envelope Forge expects from the
// platform's admission endpoint. Every field is platform-defined; the
// shape is the entire wire contract.
type admissionResponse struct {
	Decision string    `json:"decision"`           // "admit" | "deny"
	Reason   string    `json:"reason,omitempty"`   // platform-defined
	Scope    string    `json:"scope,omitempty"`    // "agent" | "workspace" | "org"
	Window   string    `json:"window,omitempty"`   // "hourly" | "daily" | "monthly" | …
	ResetAt  time.Time `json:"reset_at,omitempty"` // RFC 3339
}

// PlatformAdmissionChecker calls a platform-side admission endpoint
// per inbound request (cached for admissionCacheTTL) to decide
// whether the agent should admit new work. Built for issue #201.
//
// The checker is hard-coded to fail open: any failure path — network
// error, timeout, 4xx, 5xx, parse error — turns into a logged warning
// plus an `Allowed: true, Fallback: true` Decision that gets cached
// for the TTL. Operators who need hard enforcement on platform outage
// handle it at a different layer (ingress, NetworkPolicy). The default
// posture trades hard enforcement for availability — the cascade of
// "platform is degraded → every agent stops serving" is a worse
// production failure than "platform is degraded → quotas leak a bit
// for the duration."
//
// The cache is keyed on a single string: the agent process is
// asking-about-itself, so there's one decision in flight per process
// at any moment. agentID / orgID / workspaceID are read at startup
// from env (#157) and don't change at runtime.
type PlatformAdmissionChecker struct {
	url           string
	agentID       string
	orgID         string
	workspaceID   string
	platformToken string
	client        *http.Client
	logger        coreruntime.Logger

	// now is injected so tests can drive TTL expiration without
	// sleeping. Production callers leave it nil; the checker falls
	// back to time.Now.
	now func() time.Time

	mu       sync.Mutex
	cached   coreruntime.Decision
	cachedAt time.Time
}

// NewPlatformAdmissionChecker constructs a checker against the given
// endpoint with baked timeout + caching. Returns the checker ready to
// serve — no health-check at construction time. The first Admit call
// hits the platform; if it fails, the fallback-admit posture kicks in
// and the warn log surfaces in the operator's pipeline.
//
// agentID is required; the platform's URL routes on it. orgID and
// workspaceID are optional (empty → header omitted on the wire so
// the platform parser distinguishes "unset" from "empty string").
func NewPlatformAdmissionChecker(
	url, agentID, orgID, workspaceID, platformToken string,
	logger coreruntime.Logger,
) *PlatformAdmissionChecker {
	return &PlatformAdmissionChecker{
		url:           url,
		agentID:       agentID,
		orgID:         orgID,
		workspaceID:   workspaceID,
		platformToken: platformToken,
		client:        &http.Client{Timeout: admissionHTTPTimeout},
		logger:        logger,
	}
}

// Admit returns a Decision for the current request. Wraps the
// platform call in an admission.check OTel span; the underlying
// http.client call nests beneath it via the default transport (the
// runner installs otelhttp-wrapped transports on the egress client,
// not on this internal admission client — admission calls aren't
// counted toward LLM-provider egress).
//
// Cache semantics: hit within TTL → return cached Decision unchanged
// (Cached=true is overlaid for span / audit visibility, but the
// underlying decision is byte-identical). Miss → synchronous call,
// cache result for TTL, return it. Failure → log warn, cache an
// admit with Fallback=true for TTL.
func (c *PlatformAdmissionChecker) Admit(ctx context.Context) coreruntime.Decision {
	ctx, span := coreruntime.Tracer().Start(ctx, "admission.check")
	defer span.End()

	// Cache hit fast path. Holding the mutex across the network call
	// would serialize all inbound requests; we only hold it long
	// enough to read or write the cached decision struct.
	if cached, ok := c.fromCache(); ok {
		stampDecisionOnSpan(span, cached, true)
		return overrideCached(cached, true)
	}

	decision := c.fetchDecision(ctx)
	c.storeCache(decision)
	stampDecisionOnSpan(span, decision, false)
	return decision
}

// fromCache returns the cached Decision when it's within the TTL.
// The bool reports whether the cached value is usable. Holding the
// lock only for the read keeps the platform call out of the critical
// section.
func (c *PlatformAdmissionChecker) fromCache() (coreruntime.Decision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedAt.IsZero() {
		return coreruntime.Decision{}, false
	}
	now := c.timeNow()
	if now.Sub(c.cachedAt) >= admissionCacheTTL {
		return coreruntime.Decision{}, false
	}
	return c.cached, true
}

// storeCache stamps `now` as the cache time. The TTL window is
// measured from this point regardless of whether the stored decision
// is an admit, a deny, or a fallback-admit.
func (c *PlatformAdmissionChecker) storeCache(d coreruntime.Decision) {
	c.mu.Lock()
	c.cached = d
	c.cachedAt = c.timeNow()
	c.mu.Unlock()
}

// timeNow returns the test-overridable clock or wall time.
func (c *PlatformAdmissionChecker) timeNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// fetchDecision issues the platform call and translates its response
// into a Decision. Any failure path is converted to a fallback-admit
// (Allowed=true, Fallback=true) — caller code never branches on the
// returned error; the Fallback bool carries the signal.
func (c *PlatformAdmissionChecker) fetchDecision(ctx context.Context) coreruntime.Decision {
	reqURL, err := c.buildURL()
	if err != nil {
		return c.fallback(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return c.fallback(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.platformToken)
	// Per #201: Org-Id / Workspace-Id headers are OMITTED entirely
	// when the env value is empty — never sent as the literal empty
	// string. Lets the platform's parser distinguish "self-hosted
	// deploy without tenancy" from "platform deploy with malformed
	// tenancy" cleanly.
	if c.orgID != "" {
		req.Header.Set("Org-Id", c.orgID)
	}
	if c.workspaceID != "" {
		req.Header.Set("Workspace-Id", c.workspaceID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return c.fallback(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return c.fallback(fmt.Errorf("platform returned HTTP %d", resp.StatusCode))
	}

	var body admissionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return c.fallback(fmt.Errorf("parse: %w", err))
	}

	switch body.Decision {
	case "admit":
		return coreruntime.Decision{
			Allowed: true,
			Reason:  body.Reason,
			Scope:   body.Scope,
			Window:  body.Window,
			ResetAt: body.ResetAt,
		}
	case "deny":
		return coreruntime.Decision{
			Allowed: false,
			Reason:  body.Reason,
			Scope:   body.Scope,
			Window:  body.Window,
			ResetAt: body.ResetAt,
		}
	default:
		return c.fallback(fmt.Errorf("unknown decision %q", body.Decision))
	}
}

// buildURL appends `agent_id=<id>` to the configured URL. Lets the
// operator pass either a bare path or a URL that already carries
// query params (e.g. for canary routing).
func (c *PlatformAdmissionChecker) buildURL() (string, error) {
	u, err := url.Parse(c.url)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("agent_id", c.agentID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// fallback logs a warn and returns the fail-open Decision. Issue
// #201's central design choice: ANY failure path produces this same
// shape — admit with Fallback=true. Operators alert on
// `forge.admission.fallback=true` to surface platform outage rate.
func (c *PlatformAdmissionChecker) fallback(err error) coreruntime.Decision {
	cachedUntil := c.timeNow().Add(admissionCacheTTL).UTC().Format(time.RFC3339)
	if c.logger != nil {
		c.logger.Warn("admission: call failed, admitting", map[string]any{
			"agent_id":     c.agentID,
			"error":        err.Error(),
			"cached_until": cachedUntil,
		})
	}
	return coreruntime.Decision{
		Allowed:  true,
		Fallback: true,
	}
}

// overrideCached returns a copy of the input Decision with Cached set
// to the given value. Used to mark a decision served from the cache
// at audit / span time without mutating the cached struct itself.
func overrideCached(d coreruntime.Decision, cached bool) coreruntime.Decision {
	d.Cached = cached
	return d
}

// stampDecisionOnSpan attaches the platform-defined fields plus the
// observability flags to admission.check. Status=Error on deny so
// the error-rate dashboards count denials alongside the rest of the
// Forge span families (auth.verify, channel.deliver, schedule.fire).
func stampDecisionOnSpan(span trace.Span, d coreruntime.Decision, cached bool) {
	decision := "admit"
	if !d.Allowed {
		decision = "deny"
	}
	span.SetAttributes(
		attribute.String(observability.AttrForgeAdmissionDecision, decision),
		attribute.Bool(observability.AttrForgeAdmissionCached, cached),
		attribute.Bool(observability.AttrForgeAdmissionFallback, d.Fallback),
	)
	if d.Reason != "" {
		span.SetAttributes(attribute.String(observability.AttrForgeAdmissionReason, d.Reason))
	}
	if d.Scope != "" {
		span.SetAttributes(attribute.String(observability.AttrForgeAdmissionScope, d.Scope))
	}
	if d.Window != "" {
		span.SetAttributes(attribute.String(observability.AttrForgeAdmissionWindow, d.Window))
	}
	if !d.Allowed {
		reason := d.Reason
		if reason == "" {
			reason = "admission_denied"
		}
		span.SetStatus(codes.Error, reason)
	}
}

