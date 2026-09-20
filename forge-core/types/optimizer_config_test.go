package types

import "testing"

func TestOptimizerUpstreamFromYAML(t *testing.T) {
	// Lenient: no agent_id/version/entrypoint required (unlike ParseForgeConfig),
	// so a minimal user-global ~/.forge/forge.yaml works.
	got := OptimizerUpstreamFromYAML([]byte("optimizer:\n  upstream: https://kong.example/bedrock\n"))
	if got != "https://kong.example/bedrock" {
		t.Errorf("got %q", got)
	}

	// ${VAR} expansion.
	t.Setenv("MY_GW", "https://gw.example")
	if got := OptimizerUpstreamFromYAML([]byte("optimizer:\n  upstream: ${MY_GW}/v1\n")); got != "https://gw.example/v1" {
		t.Errorf("env expansion: got %q", got)
	}

	// Absent block → "".
	if got := OptimizerUpstreamFromYAML([]byte("agent_id: x\nversion: \"1\"\n")); got != "" {
		t.Errorf("absent should be empty, got %q", got)
	}

	// Unparseable → "" (never panics).
	if got := OptimizerUpstreamFromYAML([]byte("::: not yaml :::")); got != "" {
		t.Errorf("bad yaml should be empty, got %q", got)
	}

	// Still parses when carried in a full, valid config too.
	cfg, err := ParseForgeConfig([]byte("agent_id: a\nversion: \"1\"\nentrypoint: main.py\noptimizer:\n  upstream: https://full.example\n"))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	if cfg.Optimizer.Upstream != "https://full.example" {
		t.Errorf("full-config optimizer.upstream = %q", cfg.Optimizer.Upstream)
	}
}
