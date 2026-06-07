package runtime

import (
	"os"
	"strconv"

	"github.com/initializ/forge/forge-cli/server"
	"github.com/initializ/forge/forge-core/types"
)

// rateLimitEnvVars name the env var keys recognized by the runner.
// Exposed as constants so the CLI flag wiring + the docs reference
// the exact strings.
const (
	EnvRateLimitReadRPS      = "FORGE_RATE_LIMIT_READ_RPS"
	EnvRateLimitReadBurst    = "FORGE_RATE_LIMIT_READ_BURST"
	EnvRateLimitWriteRPS     = "FORGE_RATE_LIMIT_WRITE_RPS"
	EnvRateLimitWriteBurst   = "FORGE_RATE_LIMIT_WRITE_BURST"
	EnvRateLimitCancelExempt = "FORGE_RATE_LIMIT_CANCEL_EXEMPT"
)

// RateLimitOverride carries values from any one configuration layer
// (CLI flags, env vars, or forge.yaml). Pointer-typed fields so the
// resolver can distinguish "unset → fall through to next layer" from
// "explicitly set to zero / false" (which must win over a non-zero
// default). The cmd layer populates one of these per layer, the
// resolver merges them in precedence order.
type RateLimitOverride struct {
	ReadRPS      *float64
	ReadBurst    *int
	WriteRPS     *float64
	WriteBurst   *int
	CancelExempt *bool
}

// ResolveRateLimit merges, in precedence order:
//
//  1. CLI override (the *RateLimitOverride passed in by the cmd layer)
//  2. FORGE_RATE_LIMIT_* env vars
//  3. cfg.Server.RateLimit from forge.yaml
//  4. server.defaultRateLimitConfig (the bumped FWS-10 defaults)
//
// Returned pointer is suitable for ServerConfig.RateLimit. nil means
// "no overrides anywhere; let the server install its own defaults" —
// the common case for a forge.yaml that doesn't mention rate limits.
//
// See issue #110 / FWS-10.
func ResolveRateLimit(cfg *types.ForgeConfig, override *RateLimitOverride) *server.RateLimitConfig {
	envLayer := rateLimitFromEnv()
	yamlLayer := rateLimitFromYAML(cfg)

	// An override struct with every field nil is the same as no
	// override at all — the cmd layer can pass `&RateLimitOverride{}`
	// without intending to override anything, and we don't want that
	// to surface in the resolved config.
	if override != nil && override.ReadRPS == nil && override.ReadBurst == nil &&
		override.WriteRPS == nil && override.WriteBurst == nil && override.CancelExempt == nil {
		override = nil
	}

	if override == nil && envLayer == nil && yamlLayer == nil {
		return nil // no overrides at all → server installs its defaults
	}

	// Start from the FWS-10 defaults; subsequent layers overlay only
	// the fields they explicitly set. Keep these literals in sync
	// with server.defaultRateLimitConfig — that's the single source
	// of truth, this is a copy so the resolver doesn't import a
	// private symbol.
	out := &server.RateLimitConfig{
		ReadRPS:      1.0,
		ReadBurst:    10,
		WriteRPS:     1.0,
		WriteBurst:   20,
		CancelExempt: true,
	}
	applyLayer(out, yamlLayer)
	applyLayer(out, envLayer)
	applyLayer(out, override)
	return out
}

// rateLimitFromYAML lifts the populated subset of cfg.Server.RateLimit
// into a RateLimitOverride. Returns nil when every field is at its
// zero value (an empty `server.rate_limit:` block or no block at
// all).
func rateLimitFromYAML(cfg *types.ForgeConfig) *RateLimitOverride {
	if cfg == nil {
		return nil
	}
	y := cfg.Server.RateLimit
	if y.ReadRPS == 0 && y.ReadBurst == 0 && y.WriteRPS == 0 && y.WriteBurst == 0 && y.CancelExempt == nil {
		return nil
	}
	out := &RateLimitOverride{CancelExempt: y.CancelExempt}
	if y.ReadRPS != 0 {
		v := y.ReadRPS
		out.ReadRPS = &v
	}
	if y.ReadBurst != 0 {
		v := y.ReadBurst
		out.ReadBurst = &v
	}
	if y.WriteRPS != 0 {
		v := y.WriteRPS
		out.WriteRPS = &v
	}
	if y.WriteBurst != 0 {
		v := y.WriteBurst
		out.WriteBurst = &v
	}
	return out
}

// rateLimitFromEnv reads FORGE_RATE_LIMIT_* env vars into a layer.
// Parse failures (e.g. `READ_RPS=abc`) silently skip that field —
// the runner already warns at startup for malformed config; the env
// path fails soft so a typo doesn't lock the agent out of starting.
func rateLimitFromEnv() *RateLimitOverride {
	out := &RateLimitOverride{}
	anySet := false
	if v := os.Getenv(EnvRateLimitReadRPS); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out.ReadRPS = &f
			anySet = true
		}
	}
	if v := os.Getenv(EnvRateLimitReadBurst); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			out.ReadBurst = &n
			anySet = true
		}
	}
	if v := os.Getenv(EnvRateLimitWriteRPS); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out.WriteRPS = &f
			anySet = true
		}
	}
	if v := os.Getenv(EnvRateLimitWriteBurst); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			out.WriteBurst = &n
			anySet = true
		}
	}
	if v := os.Getenv(EnvRateLimitCancelExempt); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			out.CancelExempt = &b
			anySet = true
		}
	}
	if !anySet {
		return nil
	}
	return out
}

// applyLayer copies non-nil fields from src onto dst. nil = unset →
// dst keeps whatever the previous layer wrote. Bool semantics: an
// explicit `false` from a higher-precedence layer overrides a `true`
// default — the pointer carries the explicit-set signal.
func applyLayer(dst *server.RateLimitConfig, src *RateLimitOverride) {
	if src == nil {
		return
	}
	if src.ReadRPS != nil {
		dst.ReadRPS = *src.ReadRPS
	}
	if src.ReadBurst != nil {
		dst.ReadBurst = *src.ReadBurst
	}
	if src.WriteRPS != nil {
		dst.WriteRPS = *src.WriteRPS
	}
	if src.WriteBurst != nil {
		dst.WriteBurst = *src.WriteBurst
	}
	if src.CancelExempt != nil {
		dst.CancelExempt = *src.CancelExempt
	}
}
