package skills

import (
	"testing"
)

func TestResolve_AllFromOS(t *testing.T) {
	osEnv := map[string]string{
		"API_KEY": "key123",
		"TIMEOUT": "30",
	}
	resolver := NewEnvResolver(osEnv, nil, nil)
	reqs := &AggregatedRequirements{
		EnvRequired: []string{"API_KEY"},
		EnvOptional: []string{"TIMEOUT"},
	}

	diags := resolver.Resolve(reqs)
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics, got %d: %+v", len(diags), diags)
	}
}

func TestResolve_FallbackToDotEnv(t *testing.T) {
	osEnv := map[string]string{}
	dotEnv := map[string]string{
		"API_KEY": "from-dotenv",
	}
	resolver := NewEnvResolver(osEnv, dotEnv, nil)
	reqs := &AggregatedRequirements{
		EnvRequired: []string{"API_KEY"},
	}

	diags := resolver.Resolve(reqs)
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics, got %d: %+v", len(diags), diags)
	}
}

func TestResolve_MissingRequired_Error(t *testing.T) {
	resolver := NewEnvResolver(nil, nil, nil)
	reqs := &AggregatedRequirements{
		EnvRequired: []string{"API_KEY"},
	}

	diags := resolver.Resolve(reqs)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if diags[0].Level != "error" {
		t.Errorf("diagnostic level = %q, want error", diags[0].Level)
	}
	if diags[0].Var != "API_KEY" {
		t.Errorf("diagnostic var = %q, want API_KEY", diags[0].Var)
	}
}

func TestResolve_MissingOneOf_Error(t *testing.T) {
	resolver := NewEnvResolver(nil, nil, nil)
	reqs := &AggregatedRequirements{
		EnvOneOf: [][]string{{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"}},
	}

	diags := resolver.Resolve(reqs)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if diags[0].Level != "error" {
		t.Errorf("diagnostic level = %q, want error", diags[0].Level)
	}
}

func TestResolve_MissingOptional_Warning(t *testing.T) {
	resolver := NewEnvResolver(nil, nil, nil)
	reqs := &AggregatedRequirements{
		EnvOptional: []string{"DEBUG"},
	}

	diags := resolver.Resolve(reqs)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if diags[0].Level != "warning" {
		t.Errorf("diagnostic level = %q, want warning", diags[0].Level)
	}
}

func TestResolve_OneOfPartialSatisfied(t *testing.T) {
	osEnv := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-123",
	}
	resolver := NewEnvResolver(osEnv, nil, nil)
	reqs := &AggregatedRequirements{
		EnvOneOf: [][]string{{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"}},
	}

	diags := resolver.Resolve(reqs)
	if len(diags) != 0 {
		t.Errorf("expected 0 diagnostics, got %d: %+v", len(diags), diags)
	}
}
