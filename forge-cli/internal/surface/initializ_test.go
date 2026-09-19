package surface

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateForgeSpec(t *testing.T) {
	y, err := GenerateInitializDeploy(deployGenArgs{Type: "forge", Name: "support-agent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"apiVersion: initializ.ai/v1", "kind: AgentDeploy", "type: forge", "forge:", "path: ./forge.yaml"} {
		if !strings.Contains(y, want) {
			t.Errorf("forge spec missing %q:\n%s", want, y)
		}
	}
	// forge must NOT carry model/a2a/http.
	for _, bad := range []string{"model:", "a2a:", "http:"} {
		if strings.Contains(y, bad) {
			t.Errorf("forge spec must not contain %q:\n%s", bad, y)
		}
	}
}

func TestGenerateClaudeAgentA2A(t *testing.T) {
	// a2a is the default exposure for managed runtimes.
	y, err := GenerateInitializDeploy(deployGenArgs{Type: "claude-agent", Name: "code-reviewer", Provider: "anthropic", A2AAuth: "bearer"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type: claude-agent", "model:", "provider: anthropic", "a2a:", "enabled: true", "auth: bearer"} {
		if !strings.Contains(y, want) {
			t.Errorf("claude-agent a2a spec missing %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "forge:") || strings.Contains(y, "http:") {
		t.Errorf("claude-agent a2a spec must not contain forge/http block:\n%s", y)
	}
}

func TestGenerateClaudeAgentDefaultsToA2A(t *testing.T) {
	// No exposure fields set → default a2a.
	y, err := GenerateInitializDeploy(deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(y, "a2a:") || strings.Contains(y, "http:") {
		t.Errorf("managed default should be a2a:\n%s", y)
	}
}

func TestGenerateStrandsHTTPInvoke(t *testing.T) {
	// The invoke-endpoint (http) exposure, with a method + input schema.
	y, err := GenerateInitializDeploy(deployGenArgs{
		Type: "strands", Name: "denyabot", Provider: "anthropic", ModelName: "claude-sonnet-4-6",
		Expose: "http", HTTPPath: "/invocations", HTTPMethod: "post",
		HTTPInput: []byte(`{"type":"object","properties":{"max_apps":{"type":"integer"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type: strands", "name: claude-sonnet-4-6", "http:", "path: /invocations", "method: POST", `input: {"type":"object"`} {
		if !strings.Contains(y, want) {
			t.Errorf("strands http spec missing %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "a2a:") {
		t.Errorf("http-exposed spec must not contain a2a block:\n%s", y)
	}
}

func TestGenerateValidation(t *testing.T) {
	cases := []struct {
		name string
		args deployGenArgs
	}{
		{"unknown type", deployGenArgs{Type: "langchain", Name: "x"}},
		{"empty type", deployGenArgs{Name: "x"}},
		{"forge with provider", deployGenArgs{Type: "forge", Provider: "openai"}},
		{"forge with expose", deployGenArgs{Type: "forge", Expose: "a2a"}},
		{"forge with http", deployGenArgs{Type: "forge", HTTPPath: "/y"}},
		{"managed without name", deployGenArgs{Type: "claude-agent", Provider: "anthropic"}},
		{"managed without provider", deployGenArgs{Type: "strands", Name: "x"}},
		{"managed bad provider", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "gemini"}},
		{"expose a2a with http", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "a2a", HTTPPath: "/y"}},
		{"expose http without path", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "http"}},
		{"expose http with a2a fields", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "http", HTTPPath: "/y", A2AAuth: "bearer"}},
		{"bad expose value", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "grpc"}},
		{"conflicting a2a+http no expose", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", A2AAuth: "bearer", HTTPPath: "/y"}},
		{"http no slash", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "http", HTTPPath: "y"}},
		{"http double slash", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "http", HTTPPath: "//evil"}},
		{"http slash backslash", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "http", HTTPPath: "/\\evil"}},
		{"http bad method", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "http", HTTPPath: "/y", HTTPMethod: "FETCH"}},
		{"http bad input", deployGenArgs{Type: "claude-agent", Name: "x", Provider: "openai", Expose: "http", HTTPPath: "/y", HTTPInput: []byte("{not json")}},
		{"bad name", deployGenArgs{Type: "claude-agent", Name: "Bad_Name", Provider: "openai"}},
		{"empty env name", deployGenArgs{Type: "forge", Name: "x", Env: []deployEnvVar{{Value: "v"}}}},
	}
	for _, c := range cases {
		if _, err := GenerateInitializDeploy(c.args); err == nil {
			t.Errorf("%s: expected error, got none", c.name)
		}
	}
}

func TestInitializDeployToolWriteFalse(t *testing.T) {
	tool := initializDeployTool{base: t.TempDir()}
	out, err := tool.Execute(context.TODO(), json.RawMessage(`{"type":"forge","name":"x","write":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(out, "Wrote ") {
		t.Errorf("write:false should not write a file: %s", out)
	}
	if !strings.Contains(out, "type: forge") {
		t.Errorf("expected YAML returned: %s", out)
	}
}

func TestInitializTopicAndSearch(t *testing.T) {
	doc, ok := RenderTopic("initializ-deploy")
	if !ok || !strings.Contains(doc, "initializ-deploy.yaml") {
		t.Errorf("initializ-deploy topic not rendered: ok=%v", ok)
	}
	if !contains(TopicNames(), "initializ-deploy") {
		t.Error("initializ-deploy missing from TopicNames")
	}
	if !strings.Contains(strings.ToLower(SearchDocs("claude-agent deploy provider")), "claude-agent") {
		t.Error("search should surface initializ-deploy content")
	}
}

func TestMCPToolsetHasInitializTools(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range MCPToolset(t.TempDir()) {
		names[tool.Name()] = true
	}
	for _, want := range []string{"initializ_detect_agent", "initializ_deploy_generate"} {
		if !names[want] {
			t.Errorf("MCPToolset missing %q", want)
		}
	}
	// Generation only — the deploy passthrough must NOT be exposed.
	if names["initializ_cli"] {
		t.Error("initializ_cli must not be exposed (spec generation only, not deployment)")
	}
}

func TestDetectAgent(t *testing.T) {
	write := func(dir, name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("forge", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "forge.yaml", "agent_id: support-agent\nmodel:\n  provider: anthropic\n")
		d := DetectAgent(dir)
		if d.Type != "forge" {
			t.Errorf("type=%q, want forge", d.Type)
		}
		if !strings.Contains(strings.Join(d.Evidence, "\n"), "support-agent") {
			t.Errorf("evidence should mention agent_id: %v", d.Evidence)
		}
	})

	t.Run("claude-agent node", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "package.json", `{"dependencies":{"@anthropic-ai/claude-agent-sdk":"^1.0.0"}}`)
		d := DetectAgent(dir)
		if d.Type != "claude-agent" || d.Language != "node" {
			t.Errorf("got %q/%q, want claude-agent/node (evidence %v)", d.Type, d.Language, d.Evidence)
		}
	})

	t.Run("claude-agent node via initializ pointer", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "package.json", `{"initializ":{"a2a":"./dist/a2a-executor.js"},"dependencies":{}}`)
		if d := DetectAgent(dir); d.Type != "claude-agent" || d.Language != "node" {
			t.Errorf("got %q/%q, want claude-agent/node", d.Type, d.Language)
		}
	})

	t.Run("strands python", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "requirements.txt", "strands-agents==0.1.0\nboto3\n")
		d := DetectAgent(dir)
		if d.Type != "strands" || d.Language != "python" {
			t.Errorf("got %q/%q, want strands/python", d.Type, d.Language)
		}
	})

	t.Run("claude python pyproject", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "pyproject.toml", "[project]\ndependencies = [\"claude-agent-sdk\", \"anthropic\"]\n")
		if d := DetectAgent(dir); d.Type != "claude-agent" || d.Language != "python" {
			t.Errorf("got %q/%q, want claude-agent/python", d.Type, d.Language)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "package.json", `{"dependencies":{"express":"^4"}}`)
		if d := DetectAgent(dir); d.Type != "" {
			t.Errorf("expected unknown type, got %q", d.Type)
		}
	})

	t.Run("forge wins over deps", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "forge.yaml", "agent_id: a\n")
		write(dir, "package.json", `{"dependencies":{"@anthropic-ai/sdk":"^1"}}`)
		if d := DetectAgent(dir); d.Type != "forge" {
			t.Errorf("forge.yaml should win, got %q", d.Type)
		}
	})
}

func TestNeedsYAMLQuote(t *testing.T) {
	plain := []string{"support-agent", "registry.initializ.ai/x:latest", "${OPENAI_API_KEY}", "/invocations", "claude-sonnet-4-6", "audit_only"}
	for _, s := range plain {
		if needsYAMLQuote(s) {
			t.Errorf("%q should be a safe plain scalar", s)
		}
	}
	// Numeric-looking values must be quoted so they stay strings on the platform.
	quoted := []string{"", " leading", "trailing ", "Handles requests: weather", "a\nb", "#comment", "- dash", "true", "value:", "has # hash", "8080", "1.5", "-5", "1e3", "0x1A", "1_000"}
	for _, s := range quoted {
		if !needsYAMLQuote(s) {
			t.Errorf("%q should require quoting", s)
		}
	}
}

func TestGenerateEscapesFreeText(t *testing.T) {
	y, err := GenerateInitializDeploy(deployGenArgs{
		Type: "claude-agent", Name: "cr", Provider: "anthropic",
		A2ADescription: "Handles requests: weather, sports",
		Env:            []deployEnvVar{{Name: "MSG", Value: "line1\nINJECTED: bad"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The ": "-bearing description must be quoted (not a broken plain scalar).
	if !strings.Contains(y, `description: "Handles requests: weather, sports"`) {
		t.Errorf("description not quoted:\n%s", y)
	}
	// A newline in a value must be escaped, never a raw newline injecting a key.
	if strings.Contains(y, "INJECTED: bad\n") && !strings.Contains(y, `\nINJECTED`) {
		t.Errorf("newline injection not escaped:\n%s", y)
	}
	if !strings.Contains(y, `value: "line1\nINJECTED: bad"`) {
		t.Errorf("env value not escaped:\n%s", y)
	}
}
