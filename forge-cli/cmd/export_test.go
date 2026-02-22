package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/validate"
)

// testAgentJSON returns a valid agent.json that passes schema validation.
func testAgentJSON() string {
	spec := map[string]any{
		"forge_version":          "1.0",
		"agent_id":               "test-agent",
		"version":                "0.1.0",
		"name":                   "Test Agent",
		"description":            "A test agent",
		"tool_interface_version": "1.0",
		"skills_spec_version":    "agentskills-v1",
		"egress_profile":         "standard",
		"egress_mode":            "allowlist",
		"runtime": map[string]any{
			"image": "python:3.11-slim",
			"port":  8080,
		},
		"tools": []map[string]any{
			{
				"name":        "web-search",
				"description": "Search the web",
				"category":    "builtin",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			},
		},
		"model": map[string]any{
			"provider": "openai",
			"name":     "gpt-4",
		},
		"a2a": map[string]any{
			"capabilities": map[string]any{
				"streaming": true,
			},
		},
	}
	data, _ := json.MarshalIndent(spec, "", "  ")
	return string(data)
}

// setupExportTest creates a temp directory with forge.yaml and .forge-output/agent.json.
func setupExportTest(t *testing.T) (dir string, cleanup func()) {
	t.Helper()
	dir = t.TempDir()

	// Write forge.yaml
	writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
model:
  provider: openai
  name: gpt-4
tools:
  - name: web-search
`)

	// Create .forge-output directory
	outDir := filepath.Join(dir, ".forge-output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("creating .forge-output: %v", err)
	}

	// Write agent.json
	if err := os.WriteFile(filepath.Join(outDir, "agent.json"), []byte(testAgentJSON()), 0644); err != nil {
		t.Fatalf("writing agent.json: %v", err)
	}

	// Write build-manifest.json (so ensureBuildOutput doesn't trigger a build)
	manifest := `{"built_at":"2025-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(outDir, "build-manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("writing build-manifest.json: %v", err)
	}

	cleanup = func() {
		cfgFile = "forge.yaml"
		outputDir = "."
		exportOutput = ""
		exportPretty = false
		exportIncludeSchemas = false
		exportSimulateImport = false
		exportDevMode = false
	}

	return dir, cleanup
}

func TestRunExport_BasicExport(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "output.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	// Verify output file exists
	data, err := os.ReadFile(filepath.Join(dir, "output.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing output: %v", err)
	}

	if result["agent_id"] != "test-agent" {
		t.Errorf("agent_id = %v, want test-agent", result["agent_id"])
	}
	if result["_forge_export_meta"] == nil {
		t.Error("expected _forge_export_meta in output")
	}
}

func TestRunExport_DefaultFilename(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = "" // Use default

	// Change to temp dir so default filename is written there
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	// Default filename should be {agent_id}-forge.json
	expectedFile := filepath.Join(dir, "test-agent-forge.json")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("expected default file %s to exist", expectedFile)
	}
}

func TestRunExport_CustomFilename(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "custom-name.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "custom-name.json")); os.IsNotExist(err) {
		t.Fatal("expected custom-name.json to exist")
	}
}

func TestRunExport_PrettyFlag(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "pretty.json")
	exportPretty = true

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pretty.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	// Pretty JSON should contain indentation
	if !strings.Contains(string(data), "  ") {
		t.Error("expected indented JSON with --pretty flag")
	}
}

func TestRunExport_IncludeSchemas(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	// Create tools directory with schema
	toolsDir := filepath.Join(dir, ".forge-output", "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatalf("creating tools dir: %v", err)
	}
	schema := `{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}}}`
	if err := os.WriteFile(filepath.Join(toolsDir, "web-search.schema.json"), []byte(schema), 0644); err != nil {
		t.Fatalf("writing tool schema: %v", err)
	}

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "with-schemas.json")
	exportIncludeSchemas = true

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "with-schemas.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing output: %v", err)
	}

	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatal("expected tools array")
	}
	tool := tools[0].(map[string]any)
	inputSchema, ok := tool["input_schema"].(map[string]any)
	if !ok {
		t.Fatal("expected input_schema to be set from embedded schema")
	}
	// Should contain the "limit" property from the schema file
	props, _ := inputSchema["properties"].(map[string]any)
	if props["limit"] == nil {
		t.Error("expected embedded schema to include 'limit' property from schema file")
	}
}

func TestRunExport_SimulateImport(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportSimulateImport = true

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runExport(nil, nil)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	buf := make([]byte, 16384)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	var simResult map[string]any
	if err := json.Unmarshal([]byte(output), &simResult); err != nil {
		t.Fatalf("parsing simulate-import output: %v\noutput was: %s", err, output)
	}

	def, ok := simResult["agent_definition"].(map[string]any)
	if !ok {
		t.Fatal("expected agent_definition in output")
	}
	if def["slug"] != "test-agent" {
		t.Errorf("slug = %v, want test-agent", def["slug"])
	}
	if def["display_name"] != "Test Agent" {
		t.Errorf("display_name = %v, want Test Agent", def["display_name"])
	}
}

func TestRunExport_ExportMetaFields(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "meta.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing output: %v", err)
	}

	meta, ok := result["_forge_export_meta"].(map[string]any)
	if !ok {
		t.Fatal("expected _forge_export_meta object")
	}

	if meta["exported_at"] == nil {
		t.Error("expected exported_at in meta")
	}
	if meta["forge_cli_version"] == nil {
		t.Error("expected forge_cli_version in meta")
	}
	if meta["compatible_command_versions"] == nil {
		t.Error("expected compatible_command_versions in meta")
	}
}

func TestRunExport_RoundTrip(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "roundtrip.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	// Read exported file
	data, err := os.ReadFile(filepath.Join(dir, "roundtrip.json"))
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}

	// Strip envelope-only fields (not part of AgentSpec schema)
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("parsing export: %v", err)
	}
	delete(envelope, "_forge_export_meta")
	delete(envelope, "security")
	delete(envelope, "network_policy")

	// Re-marshal without meta
	stripped, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("re-marshalling: %v", err)
	}

	// Validate against schema
	errs, err := validate.ValidateAgentSpec(stripped)
	if err != nil {
		t.Fatalf("schema validation error: %v", err)
	}
	if len(errs) > 0 {
		t.Errorf("round-trip validation failed: %v", errs)
	}
}

// setupExportTestWithAgent creates a test directory with a custom agent.json.
func setupExportTestWithAgent(t *testing.T, agentJSON string) (dir string, cleanup func()) {
	t.Helper()
	dir = t.TempDir()

	writeTestForgeYAML(t, dir, `
agent_id: test-agent
version: 0.1.0
framework: custom
entrypoint: python agent.py
model:
  provider: openai
  name: gpt-4
tools:
  - name: web-search
`)

	outDir := filepath.Join(dir, ".forge-output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("creating .forge-output: %v", err)
	}

	if err := os.WriteFile(filepath.Join(outDir, "agent.json"), []byte(agentJSON), 0644); err != nil {
		t.Fatalf("writing agent.json: %v", err)
	}

	manifest := `{"built_at":"2025-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(outDir, "build-manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("writing build-manifest.json: %v", err)
	}

	cleanup = func() {
		cfgFile = "forge.yaml"
		outputDir = "."
		exportOutput = ""
		exportPretty = false
		exportIncludeSchemas = false
		exportSimulateImport = false
		exportDevMode = false
	}

	return dir, cleanup
}

func TestRunExport_DevToolRejection(t *testing.T) {
	agentJSON := makeAgentJSONWithDevTool()
	dir, cleanup := setupExportTestWithAgent(t, agentJSON)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "output.json")
	exportDevMode = false

	err := runExport(nil, nil)
	if err == nil {
		t.Fatal("expected error for dev-category tool without --dev flag")
	}
	if !strings.Contains(err.Error(), "dev") {
		t.Errorf("expected dev-related error, got: %v", err)
	}
}

func TestRunExport_DevToolAllowed(t *testing.T) {
	agentJSON := makeAgentJSONWithDevTool()
	dir, cleanup := setupExportTestWithAgent(t, agentJSON)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "output.json")
	exportDevMode = true

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("expected no error with --dev flag, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "output.json")); os.IsNotExist(err) {
		t.Fatal("expected output file to exist")
	}
}

func TestRunExport_SecurityBlock(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "security.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "security.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing output: %v", err)
	}

	security, ok := result["security"].(map[string]any)
	if !ok {
		t.Fatal("expected security block in envelope")
	}
	egress, ok := security["egress"].(map[string]any)
	if !ok {
		t.Fatal("expected egress in security block")
	}
	if egress["profile"] != "standard" {
		t.Errorf("egress.profile = %v, want standard", egress["profile"])
	}
	if egress["mode"] != "allowlist" {
		t.Errorf("egress.mode = %v, want allowlist", egress["mode"])
	}
}

func TestRunExport_NetworkPolicyBlock(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "netpol.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "netpol.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing output: %v", err)
	}

	np, ok := result["network_policy"].(map[string]any)
	if !ok {
		t.Fatal("expected network_policy block in envelope")
	}
	if np["default_egress"] != "deny" {
		t.Errorf("default_egress = %v, want deny", np["default_egress"])
	}
}

func TestRunExport_EnrichedMeta(t *testing.T) {
	dir, cleanup := setupExportTest(t)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "enriched.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "enriched.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing output: %v", err)
	}

	meta, ok := result["_forge_export_meta"].(map[string]any)
	if !ok {
		t.Fatal("expected _forge_export_meta object")
	}

	if meta["tool_categories"] == nil {
		t.Error("expected tool_categories in meta")
	}
	cats, ok := meta["tool_categories"].(map[string]any)
	if !ok {
		t.Fatal("expected tool_categories to be a map")
	}
	if cats["builtin"] != float64(1) {
		t.Errorf("tool_categories[builtin] = %v, want 1", cats["builtin"])
	}

	if meta["skills_count"] == nil {
		t.Error("expected skills_count in meta")
	}
	if meta["skills_count"] != float64(0) {
		t.Errorf("skills_count = %v, want 0", meta["skills_count"])
	}

	if meta["egress_profile"] != "standard" {
		t.Errorf("egress_profile = %v, want standard", meta["egress_profile"])
	}
}

func TestRunExport_RoundTripWithSkills(t *testing.T) {
	agentJSON := makeAgentJSONWithSkills()
	dir, cleanup := setupExportTestWithAgent(t, agentJSON)
	defer cleanup()

	cfgFile = filepath.Join(dir, "forge.yaml")
	outputDir = "."
	exportOutput = filepath.Join(dir, "skills-roundtrip.json")

	err := runExport(nil, nil)
	if err != nil {
		t.Fatalf("runExport() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "skills-roundtrip.json"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}

	// Parse and strip envelope-only fields
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("parsing export: %v", err)
	}

	// Verify skills are present in a2a
	a2a, ok := envelope["a2a"].(map[string]any)
	if !ok {
		t.Fatal("expected a2a in envelope")
	}
	skills, ok := a2a["skills"].([]any)
	if !ok {
		t.Fatal("expected skills array in a2a")
	}
	if len(skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(skills))
	}

	// Verify meta has skills_count
	meta, ok := envelope["_forge_export_meta"].(map[string]any)
	if !ok {
		t.Fatal("expected _forge_export_meta")
	}
	if meta["skills_count"] != float64(2) {
		t.Errorf("skills_count = %v, want 2", meta["skills_count"])
	}

	// Strip envelope-only fields and validate schema
	delete(envelope, "_forge_export_meta")
	delete(envelope, "security")
	delete(envelope, "network_policy")

	stripped, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("re-marshalling: %v", err)
	}

	errs, err := validate.ValidateAgentSpec(stripped)
	if err != nil {
		t.Fatalf("schema validation error: %v", err)
	}
	if len(errs) > 0 {
		t.Errorf("round-trip validation failed: %v", errs)
	}
}

// makeAgentJSONWithDevTool returns an agent.json with a dev-category tool.
func makeAgentJSONWithDevTool() string {
	spec := map[string]any{
		"forge_version":          "1.0",
		"agent_id":               "test-agent",
		"version":                "0.1.0",
		"name":                   "Test Agent",
		"tool_interface_version": "1.0",
		"runtime": map[string]any{
			"image": "python:3.11-slim",
			"port":  8080,
		},
		"tools": []map[string]any{
			{
				"name":        "debug-tool",
				"description": "A dev tool",
				"category":    "dev",
				"input_schema": map[string]any{
					"type": "object",
				},
			},
		},
		"model": map[string]any{
			"provider": "openai",
			"name":     "gpt-4",
		},
		"a2a": map[string]any{
			"capabilities": map[string]any{
				"streaming": true,
			},
		},
	}
	data, _ := json.MarshalIndent(spec, "", "  ")
	return string(data)
}

// makeAgentJSONWithSkills returns an agent.json with a2a skills.
func makeAgentJSONWithSkills() string {
	spec := map[string]any{
		"forge_version":          "1.0",
		"agent_id":               "test-agent",
		"version":                "0.1.0",
		"name":                   "Test Agent",
		"tool_interface_version": "1.0",
		"runtime": map[string]any{
			"image": "python:3.11-slim",
			"port":  8080,
		},
		"tools": []map[string]any{
			{
				"name":        "web-search",
				"description": "Search the web",
				"category":    "builtin",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			},
		},
		"model": map[string]any{
			"provider": "openai",
			"name":     "gpt-4",
		},
		"a2a": map[string]any{
			"skills": []map[string]any{
				{
					"id":          "pdf-processing",
					"name":        "PDF Processing",
					"description": "Process PDF documents",
				},
				{
					"id":          "data-analysis",
					"name":        "Data Analysis",
					"description": "Analyze data sets",
				},
			},
			"capabilities": map[string]any{
				"streaming": true,
			},
		},
	}
	data, _ := json.MarshalIndent(spec, "", "  ")
	return string(data)
}
