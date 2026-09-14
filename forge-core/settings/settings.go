// Package settings is the forge developer-surface configuration layer
// (issue #454): a layered settings system, modeled on Claude Code's
// settings.json + managed-settings.json, that configures what forge OFFERS
// and DEFAULTS — which channels are enabled, which model options / providers
// and the default model, which builtin tools, and the model gateway endpoint.
//
// Settings are the POSITIVE / developer surface: enablement, defaults, and
// gateway injection. They are deliberately SEPARATE from the platform policy
// layers (forge-core/security/platform_policy_layers.go), which are the
// NEGATIVE / server surface: deny / restrict / tighten, injected server-side
// by the control plane. A managed settings *lock* (an authoritative allowlist)
// is the positive counterpart to a policy *deny*; both can coexist. See #454.
package settings

import "strings"

// Settings is the merged, effective forge configuration for a session. Every
// field is optional; a zero Settings means "nothing configured" and callers
// fall back to their built-in defaults, preserving pre-#454 behavior.
type Settings struct {
	Channels ChannelSettings   `json:"channels,omitempty"`
	Models   ModelSettings     `json:"models,omitempty"`
	Tools    ToolSettings      `json:"tools,omitempty"`
	Skills   SkillSettings     `json:"skills,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

// ChannelSettings governs which channel adapters forge offers/enables in
// `forge init`, `forge run --with`, and `forge channel add`.
type ChannelSettings struct {
	// Enabled is the set of adapter names offered/enabled (e.g. "slack",
	// "telegram"). Empty means "unset" — callers use their built-in default
	// set. Merged as a union across layers.
	Enabled []string `json:"enabled,omitempty"`
}

// ModelSettings governs model provider/model defaults, the allowlist, and the
// gateway endpoint injected into the model client.
type ModelSettings struct {
	// Default is the provider+model used when none is specified explicitly
	// (flags / forge.yaml). Scalar — highest layer wins.
	Default *ModelDefault `json:"default,omitempty"`

	// AvailableModels is the allowlist of "<provider>/<model>" (or bare model)
	// identifiers a user may select. When a MANAGED layer sets it, it is the
	// authoritative allowlist (Resolve replaces the union with it) — no lower
	// layer, and no developer at runtime, can widen it: managed settings load
	// from a fixed OS path with no env/flag override (see layers.go), so on a
	// managed machine the allowlist is bounded by the OS file permissions on
	// that path. It is the positive counterpart to a policy forbidden-model
	// deny; platform policy (server-side, control-plane injected) remains the
	// defense-in-depth enforcement. When only non-managed layers set it,
	// entries are merged (union).
	AvailableModels []string `json:"available_models,omitempty"`

	// Gateway injects the model endpoint (base_url + outbound auth scheme).
	// This is how an org points every agent at its model gateway centrally
	// instead of per-agent forge.yaml. Scalar fields — highest layer wins.
	//
	// The singular Gateway is the provider-less CATCH-ALL ("one URL for all
	// providers"). For per-provider endpoints ("one URL for anthropic, one for
	// openai"), use Gateways below. `forge init`/`forge try` scaffolding reads
	// this singular field to bake base_url/auth into a generated forge.yaml;
	// the runtime overlay (#455) reads the effective set (Gateways + Gateway).
	Gateway *ModelGateway `json:"gateway,omitempty"`

	// Gateways is the list of PER-PROVIDER model gateways (#455). Each entry
	// carries a Provider it applies to (empty = provider-less catch-all). A
	// laptop's managed/user settings realistically define one per provider (the
	// dev runs an openai agent one day, an anthropic agent the next). The
	// runtime overlay picks the entry whose Provider matches the resolved model
	// provider (see GatewayForProvider); a provider match wins over the
	// catch-all. Merged per-provider (higher layer's entry for a provider wins).
	Gateways []ModelGateway `json:"gateways,omitempty"`
}

// ModelDefault is a provider+model pair.
type ModelDefault struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// ModelGateway is the model endpoint + outbound auth, mirroring the per-agent
// ModelRef fields (base_url / auth_scheme / auth_header_name; see
// forge-core/types/config.go + forge-core/llm ClientConfig).
type ModelGateway struct {
	// Provider scopes this gateway to a model provider (e.g. "openai",
	// "anthropic", "gemini"). Empty = provider-less catch-all. Used only by the
	// runtime overlay (Gateways list) to match the resolved model provider; the
	// scaffold-injection path ignores it.
	Provider       string `json:"provider,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	AuthScheme     string `json:"auth_scheme,omitempty"`
	AuthHeaderName string `json:"auth_header_name,omitempty"`

	// APIKeyHelper is an external command that prints a short-lived token to
	// stdout (the Claude Code apiKeyHelper contract, #455). When set, the
	// runtime overlay runs/caches it (by the token's JWT exp) and injects the
	// token as the model credential per AuthScheme, instead of a native static
	// API key. A MANAGED-layer helper additionally arms the login gate; a
	// user-layer helper uses the manual `forge auth login|logout|status` path.
	APIKeyHelper string `json:"api_key_helper,omitempty"`
}

// ToolSettings governs which builtin tools are offered/defaulted.
type ToolSettings struct {
	Builtins BuiltinToolSettings `json:"builtins,omitempty"`
}

// BuiltinToolSettings enumerates enabled builtin tool names. Merged as a union.
type BuiltinToolSettings struct {
	Enabled []string `json:"enabled,omitempty"`
}

// SkillSettings governs which registry skills forge offers/defaults in
// `forge init` / `forge try`. Merged as a union across layers.
type SkillSettings struct {
	// Enabled is the set of registry skill names offered/enabled (e.g.
	// "weather", "github"). Empty = unset (all registry skills offered).
	Enabled []string `json:"enabled,omitempty"`
}

// merge folds a higher-precedence layer (hi) onto a lower one (lo), returning
// the combined result. Semantics:
//   - scalars (Model default fields, Gateway fields): hi wins when non-empty,
//     else lo is kept.
//   - list keys (Channels.Enabled, Tools.Builtins.Enabled, and the non-locked
//     AvailableModels path): union (lo then hi, de-duplicated, stable order).
//   - Env maps: hi keys override lo keys.
//
// AvailableModels LOCK semantics are handled by the loader (see Resolve), not
// here: this function only performs the additive union. The loader replaces
// the union with the managed layer's authoritative list when that layer set it.
func merge(lo, hi Settings) Settings {
	out := lo

	out.Channels.Enabled = unionStrings(lo.Channels.Enabled, hi.Channels.Enabled)
	out.Tools.Builtins.Enabled = unionStrings(lo.Tools.Builtins.Enabled, hi.Tools.Builtins.Enabled)
	out.Skills.Enabled = unionStrings(lo.Skills.Enabled, hi.Skills.Enabled)
	out.Models.AvailableModels = unionStrings(lo.Models.AvailableModels, hi.Models.AvailableModels)

	out.Models.Default = mergeModelDefault(lo.Models.Default, hi.Models.Default)
	out.Models.Gateway = mergeGateway(lo.Models.Gateway, hi.Models.Gateway)
	out.Models.Gateways = mergeGateways(lo.Models.Gateways, hi.Models.Gateways)
	out.Env = mergeStringMap(lo.Env, hi.Env)

	return out
}

func mergeModelDefault(lo, hi *ModelDefault) *ModelDefault {
	if lo == nil && hi == nil {
		return nil
	}
	out := ModelDefault{}
	if lo != nil {
		out = *lo
	}
	if hi != nil {
		if hi.Provider != "" {
			out.Provider = hi.Provider
		}
		if hi.Model != "" {
			out.Model = hi.Model
		}
	}
	return &out
}

func mergeGateway(lo, hi *ModelGateway) *ModelGateway {
	if lo == nil && hi == nil {
		return nil
	}
	out := ModelGateway{}
	if lo != nil {
		out = *lo
	}
	if hi != nil {
		if hi.Provider != "" {
			out.Provider = hi.Provider
		}
		if hi.BaseURL != "" {
			out.BaseURL = hi.BaseURL
		}
		if hi.AuthScheme != "" {
			out.AuthScheme = hi.AuthScheme
		}
		if hi.AuthHeaderName != "" {
			out.AuthHeaderName = hi.AuthHeaderName
		}
		if hi.APIKeyHelper != "" {
			out.APIKeyHelper = hi.APIKeyHelper
		}
	}
	return &out
}

// mergeGateways folds two per-provider gateway lists, keyed by Provider. A
// higher-layer entry for a given provider REPLACES the lower one (whole-entry,
// not field-level — a per-provider endpoint is defined atomically). Order is
// stable: lo's providers first (in their order), then any new hi providers.
func mergeGateways(lo, hi []ModelGateway) []ModelGateway {
	if len(lo) == 0 && len(hi) == 0 {
		return nil
	}
	var order []string
	byProvider := make(map[string]ModelGateway, len(lo)+len(hi))
	add := func(list []ModelGateway) {
		for _, g := range list {
			if _, ok := byProvider[g.Provider]; !ok {
				order = append(order, g.Provider)
			}
			byProvider[g.Provider] = g
		}
	}
	add(lo)
	add(hi)
	out := make([]ModelGateway, 0, len(order))
	for _, p := range order {
		out = append(out, byProvider[p])
	}
	return out
}

// EffectiveGateways returns the runtime gateway set: the per-provider Gateways
// list plus the singular Gateway appended as a provider-less catch-all (when
// set). The runtime overlay resolves against this via GatewayForProvider.
func (m ModelSettings) EffectiveGateways() []ModelGateway {
	if len(m.Gateways) == 0 && m.Gateway == nil {
		return nil
	}
	out := make([]ModelGateway, 0, len(m.Gateways)+1)
	out = append(out, m.Gateways...)
	if m.Gateway != nil {
		out = append(out, *m.Gateway)
	}
	return out
}

// GatewayForProvider returns the gateway that applies to the given resolved
// model provider, or nil when none does. A gateway whose Provider matches
// exactly wins; otherwise the first provider-less catch-all applies. This is
// how "forge.yaml provider=openai + a gateway defining only anthropic" resolves
// to no overlay (nil) — leaving the openai run on native auth.
func (m ModelSettings) GatewayForProvider(provider string) *ModelGateway {
	gws := m.EffectiveGateways()
	// Provider names are compared case-insensitively — a config value of
	// "OpenAI" must still match a resolved "openai".
	for i := range gws {
		if gws[i].Provider != "" && strings.EqualFold(gws[i].Provider, provider) {
			return &gws[i]
		}
	}
	for i := range gws {
		if gws[i].Provider == "" {
			return &gws[i]
		}
	}
	return nil
}

// unionStrings returns lo followed by any hi entries not already present,
// preserving order and dropping duplicates. Never returns a nil-vs-empty
// distinction consumers must handle: empty input yields empty output.
func unionStrings(lo, hi []string) []string {
	if len(lo) == 0 && len(hi) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(lo)+len(hi))
	var out []string
	for _, s := range append(append([]string{}, lo...), hi...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func mergeStringMap(lo, hi map[string]string) map[string]string {
	if len(lo) == 0 && len(hi) == 0 {
		return nil
	}
	out := make(map[string]string, len(lo)+len(hi))
	for k, v := range lo {
		out[k] = v
	}
	for k, v := range hi {
		out[k] = v
	}
	return out
}
