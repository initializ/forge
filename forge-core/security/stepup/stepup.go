// Package stepup implements governance R4b — the STEP_UP
// authorization decision.
//
// Where DENY refuses an action outright and MODIFY rewrites it, a
// STEP_UP result says "this specific action requires a fresher,
// higher-assurance authentication than the caller currently has." The
// runtime aborts the tool call and returns an RFC 9470 challenge:
//
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Bearer error="step_up_required",
//	                         acr_values="<required-acr>"
//
// The caller's SDK / browser is expected to re-authenticate with a
// method that satisfies the acr requirement (an MFA prompt, a
// hardware-key ceremony, etc.) and retry the original request. On
// retry, the auth middleware validates the presented token now
// carries the required `acr` claim; the runtime admits the call.
//
// Fail-loud: an operator who lists a tool in `security.step_up.tools`
// but no caller identity has an `acr` claim gets a 401 with the
// required-acr embedded. This is intentional — the policy is that
// the tool needs step-up, and the caller not carrying acr is the
// exact case step-up is designed to catch.
package stepup

import (
	"errors"
	"fmt"
	"slices"

	"github.com/initializ/forge/forge-core/auth"
)

// Config carries the per-tool step-up requirements.
type Config struct {
	// Enabled is a master switch. When false, RequirementFor always
	// returns "" and Check is a no-op that returns nil.
	Enabled bool

	// Tools maps tool name → required acr value. Absent tools have
	// no step-up requirement.
	Tools map[string]string

	// AcrHierarchy is an optional ordered list, lowest-assurance
	// first. When present, comparison is "index-of-actual >=
	// index-of-required". When empty, comparison is strict-equal.
	AcrHierarchy []string
}

// Validate returns an error when the config would produce
// nonsensical enforcement. Called at Engine construction so the
// runner fails startup rather than at first Check.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Tools) == 0 {
		return errors.New("step_up: enabled but no tools declared")
	}
	// Every tool's required acr must appear in the hierarchy (when
	// one is declared). Catches typos.
	if len(c.AcrHierarchy) > 0 {
		known := make(map[string]bool, len(c.AcrHierarchy))
		for _, a := range c.AcrHierarchy {
			known[a] = true
		}
		for tool, req := range c.Tools {
			if !known[req] {
				return fmt.Errorf("step_up: tool %q requires acr %q which isn't in acr_hierarchy", tool, req)
			}
		}
	}
	return nil
}

// Engine evaluates step-up requirements per tool call. Immutable
// after construction — no locking needed.
type Engine struct {
	cfg       Config
	rankOfAcr map[string]int // acr → index in hierarchy; nil when strict-equal mode
}

// New constructs an Engine from cfg. Returns an error when the
// config is invalid.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var rank map[string]int
	if len(cfg.AcrHierarchy) > 0 {
		rank = make(map[string]int, len(cfg.AcrHierarchy))
		for i, a := range cfg.AcrHierarchy {
			rank[a] = i
		}
	}
	return &Engine{cfg: cfg, rankOfAcr: rank}, nil
}

// Enabled reports whether the engine is armed. Runners short-circuit
// hook registration on unconfigured deployments.
func (e *Engine) Enabled() bool { return e != nil && e.cfg.Enabled }

// RequirementFor returns the required acr value for the given tool,
// or "" when no step-up requirement applies. Callers can use this
// to skip the identity lookup when the tool doesn't need step-up.
func (e *Engine) RequirementFor(tool string) string {
	if !e.Enabled() {
		return ""
	}
	return e.cfg.Tools[tool]
}

// Check evaluates the step-up requirement for the given tool against
// the caller's identity. Returns nil when either:
//   - The tool has no step-up requirement.
//   - The identity presents an acr satisfying the requirement.
//
// Returns a *RequiredError when a step-up is required. The runner
// unwraps this to produce the RFC 9470 challenge response.
//
// A nil identity is treated as "no acr" — step-up fails closed. The
// caller MUST authenticate before the runtime evaluates step-up.
func (e *Engine) Check(tool string, identity *auth.Identity) error {
	if !e.Enabled() {
		return nil
	}
	requiredAcr := e.cfg.Tools[tool]
	if requiredAcr == "" {
		return nil // tool has no step-up requirement
	}
	presentedAcr := extractAcr(identity)
	if e.acrSatisfies(presentedAcr, requiredAcr) {
		return nil
	}
	reason := "no acr claim presented"
	if presentedAcr != "" {
		reason = fmt.Sprintf("presented acr %q does not satisfy required %q", presentedAcr, requiredAcr)
	}
	return &RequiredError{
		Tool:         tool,
		RequiredAcr:  requiredAcr,
		PresentedAcr: presentedAcr,
		Reason:       reason,
	}
}

// acrSatisfies reports whether the presented acr meets the requirement.
// Strict-equal in the default mode; hierarchy-based when configured.
func (e *Engine) acrSatisfies(presented, required string) bool {
	if presented == "" {
		return false
	}
	if e.rankOfAcr == nil {
		return presented == required
	}
	presRank, ok := e.rankOfAcr[presented]
	if !ok {
		// An acr not in the hierarchy is treated as untrusted (weaker
		// than any listed level). Conservative failure: the operator
		// declared what they know about; anything else needs to be
		// explicitly enrolled.
		return false
	}
	reqRank, ok := e.rankOfAcr[required]
	if !ok {
		// Caught at Validate; defensive here in case an out-of-band
		// mutation snuck through.
		return false
	}
	return presRank >= reqRank
}

// extractAcr pulls the `acr` claim from the identity. Falls back to
// scanning `amr` (Authentication Methods References — RFC 8176) if
// acr is absent AND the operator has declared amr values in the
// tools map. Today we only read acr; amr support is a follow-up if
// operators need it.
func extractAcr(identity *auth.Identity) string {
	if identity == nil || len(identity.Claims) == 0 {
		return ""
	}
	if v, ok := identity.Claims["acr"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// RequiredError is the typed error a step-up check returns when the
// caller's identity doesn't meet the requirement. The runner unpacks
// it via errors.As to produce the RFC 9470 challenge:
//
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Bearer error="step_up_required",
//	                         acr_values="<RequiredAcr>"
//
// The audit event uses the same fields so the SIEM can join on
// tool + required_acr.
type RequiredError struct {
	Tool         string
	RequiredAcr  string
	PresentedAcr string // may be "" when the caller had no acr claim
	Reason       string
}

func (e *RequiredError) Error() string {
	return fmt.Sprintf("step_up_required: tool=%s required_acr=%s: %s",
		e.Tool, e.RequiredAcr, e.Reason)
}

// AsRequiredError is a convenience for callers that want to check
// whether an error carries step-up semantics without importing the
// errors package at every call site. Returns (*RequiredError, true)
// on match or (nil, false) otherwise.
func AsRequiredError(err error) (*RequiredError, bool) {
	var re *RequiredError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}

// KnownAcrValues returns the acrs declared in the hierarchy (or the
// distinct set from Tools when no hierarchy is set). Used by the
// startup log so operators can confirm which levels are wired.
func (e *Engine) KnownAcrValues() []string {
	if !e.Enabled() {
		return nil
	}
	if len(e.cfg.AcrHierarchy) > 0 {
		out := make([]string, len(e.cfg.AcrHierarchy))
		copy(out, e.cfg.AcrHierarchy)
		return out
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range e.cfg.Tools {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}
