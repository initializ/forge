package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/agentspec"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
)

// findGuardrailEvent scans NDJSON audit output for the first guardrail_check
// event and returns its fields.
func findGuardrailEvent(t *testing.T, ndjson string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(ndjson), "\n") {
		if line == "" {
			continue
		}
		var e struct {
			Event  string         `json:"event"`
			Fields map[string]any `json:"fields"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Event == "guardrail_check" {
			return e.Fields
		}
	}
	t.Fatalf("no guardrail_check event in audit output:\n%s", ndjson)
	return nil
}

func buildGuard(t *testing.T, layers []security.PolicyLayer) *coreruntime.PlatformCommandGuard {
	t.Helper()
	g, err := coreruntime.NewPlatformCommandGuard(
		toPlatformCommandSpecs(security.EffectiveDeniedCommandPatterns(layers)),
	)
	if err != nil {
		t.Fatalf("build guard: %v", err)
	}
	return g
}

// TestPlatformDeniedCommand_BlocksAcrossSkills is the issue's conformance
// test: an operator pattern blocks the call at BeforeToolExec for a
// NON-cli_execute tool AND emits the attributed runtime audit event; it also
// blocks cli_execute via the reconstructed command line.
func TestPlatformDeniedCommand_BlocksAcrossSkills(t *testing.T) {
	layers := []security.PolicyLayer{
		{Source: "workspace", Path: "/ws/policy.yaml", Policy: security.PlatformPolicy{
			DeniedCommandPatterns: []agentspec.CommandFilter{
				{Pattern: `kubectl\s+delete`, Message: "destructive kubectl blocked by org policy"},
			},
		}},
	}

	var buf bytes.Buffer
	al := coreruntime.NewAuditLogger(&buf)
	r := &Runner{cfg: RunnerConfig{}, platformCommandGuard: buildGuard(t, layers)}
	hooks := coreruntime.NewHookRegistry()
	r.registerPlatformCommandGuardHook(hooks, al)

	ctx := coreruntime.WithCorrelationID(context.Background(), "corr-1")

	// Non-cli_execute tool (no active skill needed): match target is raw JSON.
	err := hooks.Fire(ctx, coreruntime.BeforeToolExec, &coreruntime.HookContext{
		ToolName:  "mcp__k8s__run",
		ToolInput: `{"cmd":"kubectl delete pod foo"}`,
	})
	if err == nil {
		t.Fatal("expected the platform pattern to block the non-cli_execute call")
	}
	if !strings.Contains(err.Error(), "org policy") {
		t.Errorf("expected the operator message in the block error, got %q", err)
	}

	f := findGuardrailEvent(t, buf.String())
	if f["source"] != "platform" {
		t.Errorf("source = %v, want platform", f["source"])
	}
	if f["decision"] != "blocked" {
		t.Errorf("decision = %v, want blocked", f["decision"])
	}
	if f["pattern"] != `kubectl\s+delete` {
		t.Errorf("pattern = %v", f["pattern"])
	}
	if f["layer"] != "workspace" {
		t.Errorf("layer = %v, want workspace", f["layer"])
	}
	if f["tool"] != "mcp__k8s__run" {
		t.Errorf("tool = %v, want mcp__k8s__run", f["tool"])
	}
	if f["message"] != "destructive kubectl blocked by org policy" {
		t.Errorf("message = %v", f["message"])
	}

	// cli_execute: reconstructed command line must also be blocked.
	if err := hooks.Fire(ctx, coreruntime.BeforeToolExec, &coreruntime.HookContext{
		ToolName:  "cli_execute",
		ToolInput: `{"binary":"kubectl","args":["delete","pod","foo"]}`,
	}); err == nil {
		t.Fatal("expected the platform pattern to block cli_execute via the command line")
	}

	// An allowed call passes through untouched.
	if err := hooks.Fire(ctx, coreruntime.BeforeToolExec, &coreruntime.HookContext{
		ToolName:  "cli_execute",
		ToolInput: `{"binary":"kubectl","args":["get","pods"]}`,
	}); err != nil {
		t.Errorf("kubectl get should pass; got %v", err)
	}
}

// TestPlatformDeniedCommand_InvalidRegexFailsClosed is the issue's second
// conformance test: a bad pattern in any layer aborts guard construction
// (which the runner surfaces as a startup error) rather than silently
// dropping the rule.
func TestPlatformDeniedCommand_InvalidRegexFailsClosed(t *testing.T) {
	layers := []security.PolicyLayer{
		{Source: "system", Path: "/etc/forge/policy.yaml", Policy: security.PlatformPolicy{
			DeniedCommandPatterns: []agentspec.CommandFilter{{Pattern: `rm\s+-rf`}},
		}},
		{Source: "workspace", Path: "/ws/policy.yaml", Policy: security.PlatformPolicy{
			DeniedCommandPatterns: []agentspec.CommandFilter{{Pattern: `(unclosed`}},
		}},
	}
	_, err := coreruntime.NewPlatformCommandGuard(
		toPlatformCommandSpecs(security.EffectiveDeniedCommandPatterns(layers)),
	)
	if err == nil {
		t.Fatal("expected guard construction to fail closed on the invalid regex")
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Errorf("error should attribute the offending layer; got %q", err)
	}
}
