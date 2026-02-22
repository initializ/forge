package validate

import (
	"encoding/json"
	"testing"
)

func TestValidateAgentSpec_Valid(t *testing.T) {
	spec := map[string]any{
		"forge_version": "1.0",
		"agent_id":      "test-agent",
		"version":       "0.1.0",
		"name":          "Test Agent",
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	errs, err := ValidateAgentSpec(data)
	if err != nil {
		t.Fatalf("ValidateAgentSpec error: %v", err)
	}
	if len(errs) > 0 {
		t.Errorf("expected no validation errors, got: %v", errs)
	}
}

func TestValidateAgentSpec_MissingRequired(t *testing.T) {
	spec := map[string]any{
		"forge_version": "1.0",
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	errs, err := ValidateAgentSpec(data)
	if err != nil {
		t.Fatalf("ValidateAgentSpec error: %v", err)
	}
	if len(errs) == 0 {
		t.Error("expected validation errors for missing required fields")
	}
}

func TestValidateAgentSpec_InvalidAgentID(t *testing.T) {
	spec := map[string]any{
		"forge_version": "1.0",
		"agent_id":      "INVALID_ID",
		"version":       "0.1.0",
		"name":          "Test",
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	errs, err := ValidateAgentSpec(data)
	if err != nil {
		t.Fatalf("ValidateAgentSpec error: %v", err)
	}
	if len(errs) == 0 {
		t.Error("expected validation errors for invalid agent_id pattern")
	}
}
