package runtime

import (
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

// FWS-10 / issue #110 resolver tests: CLI > env > yaml > defaults
// precedence; pointer semantics for "explicitly set false" on
// CancelExempt; zero-everything → nil → "use server defaults" path.

func TestResolveRateLimit_NoOverrides_ReturnsNil(t *testing.T) {
	// Bare forge.yaml + no env + no CLI → no overrides at all → nil
	// so the server installs its built-in defaults.
	cfg := &types.ForgeConfig{}
	if got := ResolveRateLimit(cfg, nil); got != nil {
		t.Errorf("expected nil (use server defaults), got %+v", got)
	}
}

func TestResolveRateLimit_YAMLOnly_AppliesAndFillsDefaults(t *testing.T) {
	cfg := &types.ForgeConfig{
		Server: types.ServerConfig{
			RateLimit: types.RateLimitYAML{
				WriteBurst: 50, // set just one field
			},
		},
	}
	got := ResolveRateLimit(cfg, nil)
	if got == nil {
		t.Fatal("expected non-nil resolution when yaml sets a field")
	}
	if got.WriteBurst != 50 {
		t.Errorf("WriteBurst = %d, want 50 (yaml override)", got.WriteBurst)
	}
	// Other fields fell through to the FWS-10 defaults.
	if got.WriteRPS != 1.0 || got.ReadRPS != 1.0 || got.ReadBurst != 10 || !got.CancelExempt {
		t.Errorf("expected defaults for un-overridden fields, got %+v", got)
	}
}

func TestResolveRateLimit_EnvBeatsYAML(t *testing.T) {
	t.Setenv(EnvRateLimitWriteRPS, "2.5")
	cfg := &types.ForgeConfig{
		Server: types.ServerConfig{
			RateLimit: types.RateLimitYAML{
				WriteRPS: 0.5, // yaml says 0.5 — env should win
			},
		},
	}
	got := ResolveRateLimit(cfg, nil)
	if got == nil || got.WriteRPS != 2.5 {
		t.Errorf("env (2.5) should beat yaml (0.5); got %+v", got)
	}
}

func TestResolveRateLimit_CLIBeatsEnvBeatsYAML(t *testing.T) {
	t.Setenv(EnvRateLimitWriteBurst, "33")
	cfg := &types.ForgeConfig{
		Server: types.ServerConfig{
			RateLimit: types.RateLimitYAML{WriteBurst: 11},
		},
	}
	cliBurst := 99
	override := &RateLimitOverride{WriteBurst: &cliBurst}
	got := ResolveRateLimit(cfg, override)
	if got == nil || got.WriteBurst != 99 {
		t.Errorf("CLI (99) should beat env (33) and yaml (11); got %+v", got)
	}
}

func TestResolveRateLimit_ExplicitFalseCancelExemptWins(t *testing.T) {
	// The default is true. yaml/env/CLI must each be able to flip
	// it to false. Pointer-bool plumbing carries the "explicitly
	// set" signal.

	// yaml: false
	yamlFalse := false
	cfg := &types.ForgeConfig{
		Server: types.ServerConfig{
			RateLimit: types.RateLimitYAML{CancelExempt: &yamlFalse},
		},
	}
	got := ResolveRateLimit(cfg, nil)
	if got == nil || got.CancelExempt {
		t.Errorf("yaml CancelExempt=false should override default true; got %+v", got)
	}

	// env: false (overrides yaml true)
	t.Setenv(EnvRateLimitCancelExempt, "false")
	yamlTrue := true
	cfg2 := &types.ForgeConfig{
		Server: types.ServerConfig{
			RateLimit: types.RateLimitYAML{CancelExempt: &yamlTrue},
		},
	}
	got2 := ResolveRateLimit(cfg2, nil)
	if got2 == nil || got2.CancelExempt {
		t.Errorf("env CancelExempt=false should beat yaml=true; got %+v", got2)
	}

	// CLI false beats env true.
	t.Setenv(EnvRateLimitCancelExempt, "true")
	cliFalse := false
	override := &RateLimitOverride{CancelExempt: &cliFalse}
	got3 := ResolveRateLimit(cfg2, override)
	if got3 == nil || got3.CancelExempt {
		t.Errorf("CLI CancelExempt=false should beat env=true; got %+v", got3)
	}
}

func TestResolveRateLimit_MalformedEnvSilentlySkipped(t *testing.T) {
	// A typo'd env value should not prevent the agent from starting;
	// the field falls through to yaml/default as if unset.
	t.Setenv(EnvRateLimitWriteRPS, "totally not a number")
	cfg := &types.ForgeConfig{
		Server: types.ServerConfig{
			RateLimit: types.RateLimitYAML{WriteRPS: 7.0},
		},
	}
	got := ResolveRateLimit(cfg, nil)
	if got == nil || got.WriteRPS != 7.0 {
		t.Errorf("malformed env should skip; yaml value (7.0) should win; got %+v", got)
	}
}

func TestResolveRateLimit_PointerSemanticsForZeroValues(t *testing.T) {
	// CLI passes ReadBurst=0 → that's a sentinel meaning "unset" in
	// our pointer-typed override (the cmd layer only sets the
	// pointer when the flag was actually passed). The resolver
	// should keep the default (10).
	override := &RateLimitOverride{} // all nil
	got := ResolveRateLimit(&types.ForgeConfig{}, override)
	// Since override is non-nil-but-empty AND there's no yaml/env,
	// the resolver returns nil per the early-return rule:
	if got != nil {
		t.Errorf("empty override + no yaml + no env should still return nil; got %+v", got)
	}
}
