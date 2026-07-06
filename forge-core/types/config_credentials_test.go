package types

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/credentials"
	// registers the static provider so InjectorResolveSpec can build one
	_ "github.com/initializ/forge/forge-core/credentials/static"
)

// TestParseForgeConfig_CredentialsBlock is the regression test for
// @initializ-mk's #236 second-round blocker: the operator-facing
// `credentials:` block must round-trip through forge.yaml →
// ParseForgeConfig → credentials.CredentialSpec → credentials.Injector.
//
// Pre-fix, `CredentialSpec.Spec` was a raw `json.RawMessage` with
// yaml.v3 default decoding, which yaml.v3 rejected with
// "cannot unmarshal !!map into json.RawMessage" — the whole feature
// was unusable through its only interface. This test locks the
// UnmarshalYAML fix in place.
func TestParseForgeConfig_CredentialsBlock(t *testing.T) {
	src := `
agent_id: r9-demo
version: 0.1.0
framework: forge
entrypoint: main.py
security:
  step_up:
    enabled: false
credentials:
  - tool: cli_execute
    binary: env
    provider: static
    spec:
      env:
        JIT_TOKEN: jit-secret-abc123
        AWS_ACCESS_KEY_ID: AKIAJITSCOPED
      ttl: 15m
  - tool: http_request
    provider: static
    spec:
      headers:
        Authorization: Bearer jit-header
        X-Jit-Auth: yes
`
	cfg, err := ParseForgeConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	if len(cfg.Credentials) != 2 {
		t.Fatalf("expected 2 credential specs, got %d", len(cfg.Credentials))
	}

	// First spec: cli_execute env injector.
	c0 := cfg.Credentials[0]
	if c0.Tool != "cli_execute" || c0.Binary != "env" || c0.Provider != "static" {
		t.Errorf("spec[0] wrong outer fields: %+v", c0)
	}
	if len(c0.Spec) == 0 {
		t.Fatal("spec[0].Spec is empty — YAML sub-node dropped")
	}
	// The re-encoded JSON must contain the operator's map. yaml.v3
	// produces map[string]interface{} → json.Marshal → JSON with
	// alphabetical keys. Assert on substrings so key order doesn't
	// matter.
	c0JSON := string(c0.Spec)
	for _, need := range []string{
		`"JIT_TOKEN":"jit-secret-abc123"`,
		`"AWS_ACCESS_KEY_ID":"AKIAJITSCOPED"`,
		`"ttl":"15m"`,
	} {
		if !strings.Contains(c0JSON, need) {
			t.Errorf("spec[0].Spec missing %q\nfull: %s", need, c0JSON)
		}
	}

	// Second spec: http_request headers injector, no binary field.
	c1 := cfg.Credentials[1]
	if c1.Tool != "http_request" || c1.Binary != "" || c1.Provider != "static" {
		t.Errorf("spec[1] wrong outer fields: %+v", c1)
	}
	if !strings.Contains(string(c1.Spec), `"Authorization":"Bearer jit-header"`) {
		t.Errorf("spec[1] missing Authorization header: %s", c1.Spec)
	}
}

// TestParseForgeConfig_Credentials_BuildsInjector proves the fix at
// end-to-end depth: parse yaml → build a live Injector (via the
// static provider registered above through the blank import).
// Without the UnmarshalYAML fix the parse step errored; with the
// fix the round-trip through NewInjector must also work — that
// asserts the JSON round-trip produces valid provider spec bytes.
func TestParseForgeConfig_Credentials_BuildsInjector(t *testing.T) {
	src := `
agent_id: r9-demo
version: 0.1.0
framework: forge
entrypoint: main.py
credentials:
  - tool: cli_execute
    provider: static
    spec:
      env: { K1: v1, K2: v2 }
`
	cfg, err := ParseForgeConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	inj, err := credentials.NewInjector(context.Background(), credentials.DefaultRegistry, cfg.Credentials, nil)
	if err != nil {
		t.Fatalf("NewInjector: %v", err)
	}
	if inj.Empty() {
		t.Fatal("Injector unexpectedly empty after resolving 1 spec")
	}
}

// TestParseForgeConfig_CredentialsEmptySpec — a spec with the
// `spec:` key absent must not error. Some providers infer
// everything from env (e.g. sts_assume_role with source_* env vars).
func TestParseForgeConfig_CredentialsEmptySpec(t *testing.T) {
	src := `
agent_id: r9-demo
version: 0.1.0
framework: forge
entrypoint: main.py
credentials:
  - tool: cli_execute
    provider: static
`
	cfg, err := ParseForgeConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	if len(cfg.Credentials) != 1 {
		t.Fatalf("want 1 spec, got %d", len(cfg.Credentials))
	}
	if len(cfg.Credentials[0].Spec) != 0 {
		t.Errorf("empty spec must decode to nil RawMessage, got: %s", cfg.Credentials[0].Spec)
	}
}

// TestCredentialSpec_JSONRoundTripIndependence — the JSON path
// (used by the OCI packaging pipeline + tests) still works
// unchanged. UnmarshalYAML must not have broken the JSON side.
func TestCredentialSpec_JSONRoundTripIndependence(t *testing.T) {
	original := credentials.CredentialSpec{
		Tool:     "cli_execute",
		Binary:   "env",
		Provider: "static",
		Spec:     json.RawMessage(`{"env":{"K":"v"}}`),
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back credentials.CredentialSpec
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Tool != original.Tool || back.Binary != original.Binary || back.Provider != original.Provider {
		t.Errorf("JSON round trip lost outer fields: got %+v want %+v", back, original)
	}
	if !strings.Contains(string(back.Spec), `"K":"v"`) {
		t.Errorf("JSON round trip lost spec: %s", back.Spec)
	}
}
