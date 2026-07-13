package security

import (
	"fmt"
	"os"
	"strings"

	"github.com/initializ/forge/forge-core/agentspec"
	"gopkg.in/yaml.v3"
)

// PlatformPolicy is the workspace-level runtime safety net the platform
// (initializ Command, custom deployers, GitOps controllers) injects at
// deploy time to bound what a forge.yaml is allowed to declare. The
// agent's forge.yaml is what it *claims* to do; the platform policy is
// the *ceiling* — the agent refuses to start if its declaration
// exceeds the ceiling. See issue #89 / FWS-5.
//
// Absence of a policy file is the normal case for self-managed
// deployments and `forge run`: when FORGE_PLATFORM_POLICY is unset
// (or points at a missing file), the loader returns a zero-value
// policy that constrains nothing — fully backward compatible with
// pre-FWS-5 behavior.
//
// The policy is read **once at startup**. Live reload is deliberately
// out of scope for v1: policy changes require a redeploy. This keeps
// the trust boundary predictable — the agent's running state always
// reflects the policy file that was present at boot.
//
// Schema sharing with FWS-6 (#90): the DeniedChannels slot is
// reserved here so operators read the policy as a single document.
// Until FWS-6 ships, the field exists but is not consumed by the
// runtime.
type PlatformPolicy struct {
	// DeniedEgressDomains is the workspace-level deny list applied on
	// top of forge.yaml's egress.allowed_domains. At startup, the
	// effective allowlist is the set-difference (forge.yaml allowed
	// MINUS this list). If forge.yaml's allow list contains any domain
	// in this set, the agent refuses to start with a clear error and
	// emits policy_violation_at_build_time — operators see the
	// conflict in their audit pipeline, developers see the error.
	//
	// Domain matching is exact-host (no wildcards in this list).
	// Wildcard semantics belong in forge.yaml; the platform deny list
	// is operator-supplied and intentionally simple.
	DeniedEgressDomains []string `yaml:"denied_egress_domains,omitempty" json:"denied_egress_domains,omitempty"`

	// DeniedTools is the union with forge.yaml's denied tools. Tool
	// names match the registry name (e.g. "cli_execute", "http_request",
	// MCP-namespaced "linear__create_issue"). A tool denied here is
	// stripped from the agent's registry at startup — same code path
	// as forge.yaml's deny list.
	DeniedTools []string `yaml:"denied_tools,omitempty" json:"denied_tools,omitempty"`

	// ForbiddenModels lists provider/name pairs the agent must NOT
	// use. If forge.yaml's model OR any model in model.fallbacks
	// matches an entry here, the agent refuses to start. Use cases:
	// cost ceilings ("no Opus in this workspace"), data-residency
	// requirements ("no third-party providers for tenant X").
	ForbiddenModels []ModelMatcher `yaml:"forbidden_models,omitempty" json:"forbidden_models,omitempty"`

	// MaxEgressAllowlistSize caps the number of entries forge.yaml's
	// egress.allowed_domains may declare. Defense against allowlist
	// bloat — a developer adding 200 third-party domains to their
	// allowlist is almost certainly doing something they shouldn't.
	// Zero means no cap (today's behavior).
	MaxEgressAllowlistSize int `yaml:"max_egress_allowlist_size,omitempty" json:"max_egress_allowlist_size,omitempty"`

	// MaxToolCount caps the number of tools the agent may register
	// (after intersection/union math). Same rationale as
	// MaxEgressAllowlistSize. Zero means no cap.
	MaxToolCount int `yaml:"max_tool_count,omitempty" json:"max_tool_count,omitempty"`

	// DeniedChannels reserved for FWS-6 (#90) — channel-policy
	// injection. Not consumed by the runtime in v1; the field is on
	// the schema so operators write one policy document, not two.
	DeniedChannels []string `yaml:"denied_channels,omitempty" json:"denied_channels,omitempty"`

	// DeniedCommandPatterns is an operator-authored, argument-level
	// command denylist applied to EVERY tool call by ANY skill the agent
	// uses (#238 / ASI02). It gives operators org-wide "keep cli_execute
	// but ban `rm -rf` / `git push --force` / `kubectl delete`" control
	// that today only skill authors can express via SKILL.md deny_commands.
	//
	// UNIQUE among PlatformPolicy fields: every other field is enforced
	// ONCE at startup (registry strip / allowlist diff / refuse-to-start).
	// This one is enforced PER INVOCATION — matched at BeforeToolExec on
	// each call's arguments with the same match target as skill
	// deny_commands (cli_execute → reconstructed command line; any other
	// tool → raw tool-input JSON). The tool is NOT stripped; only matching
	// calls are blocked, and a block emits a runtime guardrail_check audit
	// event tagged source: platform with first-denying-layer attribution.
	//
	// Unioned across layers like the other deny lists; a skill's own
	// deny_commands cannot relax an operator pattern (compose =
	// most-restrictive-wins). Patterns are compiled at startup and an
	// invalid regex fails closed (aborts startup), matching the loud-fail
	// posture of the other policy fields. Reuses agentspec.CommandFilter so
	// operators and skill authors author patterns identically and attach an
	// optional custom deny message.
	DeniedCommandPatterns []agentspec.CommandFilter `yaml:"denied_command_patterns,omitempty" json:"denied_command_patterns,omitempty"`

	// Guardrails is the platform guardrails OVERLAY (#284) — a
	// most-restrictive layer merged over the agent's guardrails.json. It
	// uses the exact same schema as guardrails.json
	// (guardrails.StructuredGuardrails: pii / security / customRules /
	// gateConfig / …), authored here in YAML with the same camelCase
	// field names.
	//
	// It is held as a raw subtree (map[string]any) rather than the typed
	// struct so forge-core stays free of a dependency on the external
	// guardrails module — forge-cli bridges this YAML subtree into the
	// typed StructuredGuardrails (YAML → JSON → struct) and applies the
	// one-way tighten merge. The platform can only tighten (force
	// detections/gates on, raise actions, lower thresholds, union
	// rule/denylist/blocked-skill sets); it can never loosen.
	Guardrails map[string]any `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
}

// ModelMatcher identifies one forbidden model. Both fields are
// required; "match any model from provider X" intentionally requires
// listing every model name — operators must be explicit. Loose
// patterns ("anthropic/*") are a footgun (provider adds a new model,
// nobody updates the policy, model leaks through).
type ModelMatcher struct {
	Provider string `yaml:"provider" json:"provider"`
	Name     string `yaml:"name" json:"name"`
}

// String returns "provider/name" for log + error message use.
func (m ModelMatcher) String() string {
	return m.Provider + "/" + m.Name
}

// IsZero reports whether the policy applies any constraint. A
// zero-value policy is what callers get when no FORGE_PLATFORM_POLICY
// is set; IsZero lets the runtime skip enforcement entirely (and skip
// emitting the policy_loaded audit event) for the common
// "no platform policy" case.
func (p PlatformPolicy) IsZero() bool {
	return len(p.DeniedEgressDomains) == 0 &&
		len(p.DeniedTools) == 0 &&
		len(p.ForbiddenModels) == 0 &&
		p.MaxEgressAllowlistSize == 0 &&
		p.MaxToolCount == 0 &&
		len(p.DeniedChannels) == 0 &&
		len(p.DeniedCommandPatterns) == 0 &&
		len(p.Guardrails) == 0
}

// LoadPlatformPolicy reads + parses a platform policy file from disk.
// An empty path or a missing file returns the zero-value policy with
// no error — both map to "no platform policy applied" by design.
// Parse errors and schema-validation errors are returned so the
// caller can fail startup loudly (a malformed policy file is an
// operator mistake that must NOT default to "no policy" — that's the
// opposite of safe).
func LoadPlatformPolicy(path string) (PlatformPolicy, error) {
	if path == "" {
		return PlatformPolicy{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// `optional: true` ConfigMap mount produces an empty mount
			// when the operator hasn't created the ConfigMap. Treat
			// missing file as "no policy" so the manifests can be
			// policy-ready by default without forcing operators to
			// create the ConfigMap.
			return PlatformPolicy{}, nil
		}
		return PlatformPolicy{}, fmt.Errorf("reading platform policy %s: %w", path, err)
	}
	return ParsePlatformPolicy(data)
}

// ParsePlatformPolicy parses a YAML byte slice into a PlatformPolicy
// and validates the result. Exposed separately from LoadPlatformPolicy
// so `forge validate --platform-policy` can lint a policy without
// touching the filesystem twice and so tests don't need a tempdir.
func ParsePlatformPolicy(data []byte) (PlatformPolicy, error) {
	var p PlatformPolicy
	// Strict decoding: unknown fields are operator typos that must
	// fail loudly. The policy is an operator-supplied document; a
	// silently-ignored "deinied_tools: ..." (typo) would be a security
	// regression.
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		// Empty YAML decode error is "EOF" — treat as empty document
		// (zero policy). Anything else is a real parse error.
		if err.Error() == "EOF" {
			return PlatformPolicy{}, nil
		}
		return PlatformPolicy{}, fmt.Errorf("parsing platform policy: %w", err)
	}
	if err := p.Validate(); err != nil {
		return PlatformPolicy{}, err
	}
	return p, nil
}

// Validate reports schema-level errors in the policy document itself
// (separate from "forge.yaml conflicts with policy" which is the
// runtime's enforcement check). Used by `forge validate
// --platform-policy` and by the loader.
func (p PlatformPolicy) Validate() error {
	for i, m := range p.ForbiddenModels {
		if m.Provider == "" {
			return fmt.Errorf("forbidden_models[%d]: provider is required", i)
		}
		if m.Name == "" {
			return fmt.Errorf("forbidden_models[%d]: name is required", i)
		}
	}
	if p.MaxEgressAllowlistSize < 0 {
		return fmt.Errorf("max_egress_allowlist_size must be >= 0, got %d", p.MaxEgressAllowlistSize)
	}
	if p.MaxToolCount < 0 {
		return fmt.Errorf("max_tool_count must be >= 0, got %d", p.MaxToolCount)
	}
	return nil
}

// EgressDomainDenied reports whether the given domain is on the
// platform deny list. Case-insensitive exact match — domain values in
// forge.yaml are normalized at parse time, but the platform deny list
// comes straight from an operator's YAML and we don't want a
// case-difference to slip something through.
func (p PlatformPolicy) EgressDomainDenied(domain string) bool {
	want := strings.ToLower(strings.TrimSpace(domain))
	for _, d := range p.DeniedEgressDomains {
		if strings.ToLower(strings.TrimSpace(d)) == want {
			return true
		}
	}
	return false
}

// ToolDenied reports whether the given tool name is on the platform
// deny list. Tool names are case-sensitive in the registry, so this
// match is case-sensitive too — "cli_execute" and "CLI_Execute" are
// different identifiers.
func (p PlatformPolicy) ToolDenied(name string) bool {
	for _, t := range p.DeniedTools {
		if t == name {
			return true
		}
	}
	return false
}

// ModelForbidden reports whether the given provider/name pair matches
// any entry in ForbiddenModels. Used to check both the primary model
// and every fallback.
func (p PlatformPolicy) ModelForbidden(provider, name string) bool {
	for _, m := range p.ForbiddenModels {
		if m.Provider == provider && m.Name == name {
			return true
		}
	}
	return false
}

// ChannelDenied reports whether the given channel name is on the
// platform deny list. Match is case-sensitive — channel names are
// registry identifiers (e.g. "slack", "telegram", "msteams"), same
// convention as tool names. Used by the runtime at channel adapter
// init to skip denied channels and emit a channel_denied_by_policy
// audit event. See issue #90 / FWS-6.
func (p PlatformPolicy) ChannelDenied(name string) bool {
	for _, c := range p.DeniedChannels {
		if c == name {
			return true
		}
	}
	return false
}
