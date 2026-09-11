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

// Settings is the merged, effective forge configuration for a session. Every
// field is optional; a zero Settings means "nothing configured" and callers
// fall back to their built-in defaults, preserving pre-#454 behavior.
type Settings struct {
	Channels ChannelSettings   `json:"channels,omitempty"`
	Models   ModelSettings     `json:"models,omitempty"`
	Tools    ToolSettings      `json:"tools,omitempty"`
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
	// identifiers a user may select. When a MANAGED layer sets it, it is a
	// hard LOCK (authoritative allowlist, not merged) — the positive
	// counterpart to a policy forbidden-model deny. When only non-managed
	// layers set it, entries are merged (union).
	AvailableModels []string `json:"available_models,omitempty"`

	// Gateway injects the model endpoint (base_url + outbound auth scheme).
	// This is how an org points every agent at its model gateway centrally
	// instead of per-agent forge.yaml. Scalar fields — highest layer wins.
	Gateway *ModelGateway `json:"gateway,omitempty"`
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
	BaseURL        string `json:"base_url,omitempty"`
	AuthScheme     string `json:"auth_scheme,omitempty"`
	AuthHeaderName string `json:"auth_header_name,omitempty"`
}

// ToolSettings governs which builtin tools are offered/defaulted.
type ToolSettings struct {
	Builtins BuiltinToolSettings `json:"builtins,omitempty"`
}

// BuiltinToolSettings enumerates enabled builtin tool names. Merged as a union.
type BuiltinToolSettings struct {
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
	out.Models.AvailableModels = unionStrings(lo.Models.AvailableModels, hi.Models.AvailableModels)

	out.Models.Default = mergeModelDefault(lo.Models.Default, hi.Models.Default)
	out.Models.Gateway = mergeGateway(lo.Models.Gateway, hi.Models.Gateway)
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
		if hi.BaseURL != "" {
			out.BaseURL = hi.BaseURL
		}
		if hi.AuthScheme != "" {
			out.AuthScheme = hi.AuthScheme
		}
		if hi.AuthHeaderName != "" {
			out.AuthHeaderName = hi.AuthHeaderName
		}
	}
	return &out
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
