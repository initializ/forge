package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-cli/config"
)

func TestParseSkillsFileHeadings(t *testing.T) {
	content := `# My Agent Skills

## Tool: web_search

A tool for searching the web.

## Tool: sql_query

A tool for running SQL queries.
`
	path := writeTempFile(t, "skills.md", content)
	tools, err := parseSkillsFile(path)
	if err != nil {
		t.Fatalf("parseSkillsFile error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "web_search" {
		t.Errorf("expected tool[0].Name = web_search, got %q", tools[0].Name)
	}
	if tools[1].Name != "sql_query" {
		t.Errorf("expected tool[1].Name = sql_query, got %q", tools[1].Name)
	}
}

func TestParseSkillsFileListItems(t *testing.T) {
	content := `# Tools

- calculator
- translator
`
	path := writeTempFile(t, "skills.md", content)
	tools, err := parseSkillsFile(path)
	if err != nil {
		t.Fatalf("parseSkillsFile error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "calculator" {
		t.Errorf("expected tool[0].Name = calculator, got %q", tools[0].Name)
	}
	if tools[1].Name != "translator" {
		t.Errorf("expected tool[1].Name = translator, got %q", tools[1].Name)
	}
}

func TestParseSkillsFileMixed(t *testing.T) {
	content := `# Skills

## Tool: api_client

Calls APIs.

# Other

- helper_util
`
	path := writeTempFile(t, "skills.md", content)
	tools, err := parseSkillsFile(path)
	if err != nil {
		t.Fatalf("parseSkillsFile error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "api_client" {
		t.Errorf("expected tool[0].Name = api_client, got %q", tools[0].Name)
	}
	if tools[1].Name != "helper_util" {
		t.Errorf("expected tool[1].Name = helper_util, got %q", tools[1].Name)
	}
}

func TestCollectNonInteractiveMissingName(t *testing.T) {
	opts := &initOptions{Framework: "custom", ModelProvider: "openai", EnvVars: map[string]string{}}
	err := collectNonInteractive(opts)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestCollectNonInteractiveFrameworkDefaults(t *testing.T) {
	opts := &initOptions{Name: "test", ModelProvider: "openai", EnvVars: map[string]string{}}
	err := collectNonInteractive(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Framework != "custom" {
		t.Errorf("expected framework custom, got %q", opts.Framework)
	}
	if opts.Language != "python" {
		t.Errorf("expected language python, got %q", opts.Language)
	}
}

func TestCollectNonInteractiveMissingProvider(t *testing.T) {
	opts := &initOptions{Name: "test", EnvVars: map[string]string{}}
	err := collectNonInteractive(opts)
	if err == nil {
		t.Fatal("expected error for missing model-provider")
	}
}

func TestCollectNonInteractiveInvalidFramework(t *testing.T) {
	opts := &initOptions{Name: "test", Framework: "invalid", ModelProvider: "openai", EnvVars: map[string]string{}}
	err := collectNonInteractive(opts)
	if err == nil {
		t.Fatal("expected error for invalid framework")
	}
}

func TestCollectNonInteractiveCrewAIGoLanguage(t *testing.T) {
	opts := &initOptions{Name: "test", Framework: "crewai", Language: "go", ModelProvider: "openai", EnvVars: map[string]string{}}
	err := collectNonInteractive(opts)
	if err == nil {
		t.Fatal("expected error for crewai with go language")
	}
}

func TestCollectNonInteractiveLangchainTypeScript(t *testing.T) {
	opts := &initOptions{Name: "test", Framework: "langchain", Language: "typescript", ModelProvider: "openai", EnvVars: map[string]string{}}
	err := collectNonInteractive(opts)
	if err == nil {
		t.Fatal("expected error for langchain with typescript language")
	}
}

func TestCollectNonInteractiveCustomDefaults(t *testing.T) {
	opts := &initOptions{Name: "test", ModelProvider: "openai", EnvVars: map[string]string{}}
	err := collectNonInteractive(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Framework != "custom" {
		t.Errorf("expected default framework custom, got %q", opts.Framework)
	}
	if opts.Language != "python" {
		t.Errorf("expected default language python, got %q", opts.Language)
	}
}

func TestCollectNonInteractive_WithTools(t *testing.T) {
	opts := &initOptions{
		Name:          "test",
		ModelProvider: "openai",
		BuiltinTools:  []string{"web_search", "http_request"},
		EnvVars:       map[string]string{},
	}
	err := collectNonInteractive(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts.BuiltinTools) != 2 {
		t.Errorf("expected 2 builtin tools, got %d", len(opts.BuiltinTools))
	}
}

func TestCollectNonInteractive_WithSkills(t *testing.T) {
	opts := &initOptions{
		Name:          "test",
		ModelProvider: "openai",
		Skills:        []string{"github", "weather"},
		EnvVars:       map[string]string{},
	}
	err := collectNonInteractive(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts.Skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(opts.Skills))
	}
}

func TestCollectNonInteractive_RequiresName(t *testing.T) {
	opts := &initOptions{
		Framework:     "custom",
		ModelProvider: "openai",
		EnvVars:       map[string]string{},
	}
	err := collectNonInteractive(opts)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected error about --name, got: %v", err)
	}
}

func TestGetFileManifestCrewAI(t *testing.T) {
	opts := &initOptions{Framework: "crewai", Language: "python"}
	files := getFileManifest(opts)
	assertContainsTemplate(t, files, "crewai/agent.py.tmpl")
	assertContainsTemplate(t, files, "crewai/example_tool.py.tmpl")
}

func TestGetFileManifestLangchain(t *testing.T) {
	opts := &initOptions{Framework: "langchain", Language: "python"}
	files := getFileManifest(opts)
	assertContainsTemplate(t, files, "langchain/agent.py.tmpl")
	assertContainsTemplate(t, files, "langchain/example_tool.py.tmpl")
}

func TestGetFileManifestCustomPython(t *testing.T) {
	opts := &initOptions{Framework: "custom", Language: "python"}
	files := getFileManifest(opts)
	assertContainsTemplate(t, files, "custom/agent.py.tmpl")
	assertContainsTemplate(t, files, "custom/example_tool.py.tmpl")
}

func TestGetFileManifestCustomTypeScript(t *testing.T) {
	opts := &initOptions{Framework: "custom", Language: "typescript"}
	files := getFileManifest(opts)
	assertContainsTemplate(t, files, "custom/agent.ts.tmpl")
	assertContainsTemplate(t, files, "custom/example_tool.ts.tmpl")
}

func TestGetFileManifestCustomGo(t *testing.T) {
	opts := &initOptions{Framework: "custom", Language: "go"}
	files := getFileManifest(opts)
	assertContainsTemplate(t, files, "custom/main.go.tmpl")
	assertContainsTemplate(t, files, "custom/example_tool.go.tmpl")
}

func TestGetFileManifestCommonFiles(t *testing.T) {
	opts := &initOptions{Framework: "custom", Language: "python"}
	files := getFileManifest(opts)
	assertContainsTemplate(t, files, "forge.yaml.tmpl")
	assertContainsTemplate(t, files, "skills.md.tmpl")
	assertContainsTemplate(t, files, "env.example.tmpl")
	assertContainsTemplate(t, files, "gitignore.tmpl")
}

func TestScaffoldIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:          "Test Agent",
		AgentID:       "test-agent",
		Framework:     "custom",
		Language:      "go",
		ModelProvider: "openai",
		Channels:      []string{"slack"},
		Tools: []toolEntry{
			{Name: "web_search", Type: "custom"},
		},
		EnvVars:        map[string]string{},
		NonInteractive: true,
	}

	err := scaffold(opts)
	if err != nil {
		t.Fatalf("scaffold error: %v", err)
	}

	// Verify all expected files exist
	expectedFiles := []string{
		"forge.yaml",
		"main.go",
		"tools/example_tool.go",
		"skills.md",
		".env.example",
		".gitignore",
	}

	for _, f := range expectedFiles {
		path := filepath.Join("test-agent", f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", path)
		}
	}

	// Verify forge.yaml is parseable by LoadForgeConfig
	cfg, err := config.LoadForgeConfig(filepath.Join("test-agent", "forge.yaml"))
	if err != nil {
		t.Fatalf("LoadForgeConfig error: %v", err)
	}
	if cfg.AgentID != "test-agent" {
		t.Errorf("expected agent_id = test-agent, got %q", cfg.AgentID)
	}
	if cfg.Framework != "custom" {
		t.Errorf("expected framework = custom, got %q", cfg.Framework)
	}
	if cfg.Entrypoint != "go run main.go" {
		t.Errorf("expected entrypoint = 'go run main.go', got %q", cfg.Entrypoint)
	}
	if cfg.Model.Provider != "openai" {
		t.Errorf("expected model.provider = openai, got %q", cfg.Model.Provider)
	}
	if len(cfg.Channels) != 1 || cfg.Channels[0] != "slack" {
		t.Errorf("expected channels = [slack], got %v", cfg.Channels)
	}
	if len(cfg.Tools) != 1 || cfg.Tools[0].Name != "web_search" {
		t.Errorf("expected tools = [{web_search custom}], got %v", cfg.Tools)
	}
}

func TestScaffoldLangchainWithSkills(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:          "My Agent",
		AgentID:       "my-agent",
		Framework:     "langchain",
		Language:      "python",
		ModelProvider: "anthropic",
		Tools: []toolEntry{
			{Name: "api_caller", Type: "custom"},
		},
		EnvVars:        map[string]string{},
		NonInteractive: true,
	}

	err := scaffold(opts)
	if err != nil {
		t.Fatalf("scaffold error: %v", err)
	}

	cfg, err := config.LoadForgeConfig(filepath.Join("my-agent", "forge.yaml"))
	if err != nil {
		t.Fatalf("LoadForgeConfig error: %v", err)
	}
	if cfg.Entrypoint != "python agent.py" {
		t.Errorf("expected entrypoint = 'python agent.py', got %q", cfg.Entrypoint)
	}
	if cfg.Model.Provider != "anthropic" {
		t.Errorf("expected model.provider = anthropic, got %q", cfg.Model.Provider)
	}
}

func TestScaffold_GeneratesEnvFile(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:           "env-test",
		AgentID:        "env-test",
		Framework:      "custom",
		Language:       "python",
		ModelProvider:  "openai",
		APIKey:         "sk-test123",
		EnvVars:        map[string]string{"OPENAI_API_KEY": "sk-test123"},
		NonInteractive: true,
	}

	err := scaffold(opts)
	if err != nil {
		t.Fatalf("scaffold error: %v", err)
	}

	envPath := filepath.Join("env-test", ".env")
	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	if !strings.Contains(string(content), "OPENAI_API_KEY=sk-test123") {
		t.Errorf("expected .env to contain OPENAI_API_KEY=sk-test123, got:\n%s", content)
	}
}

func TestScaffold_VendorsSkills(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:           "skill-test",
		AgentID:        "skill-test",
		Framework:      "custom",
		Language:       "python",
		ModelProvider:  "openai",
		Skills:         []string{"github"},
		EnvVars:        map[string]string{},
		NonInteractive: true,
	}

	err := scaffold(opts)
	if err != nil {
		t.Fatalf("scaffold error: %v", err)
	}

	skillPath := filepath.Join("skill-test", "skills", "github.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("reading vendored skill: %v", err)
	}
	if !strings.Contains(string(content), "## Tool: github_create_issue") {
		t.Errorf("vendored github.md missing expected tool heading")
	}
}

func TestScaffold_EgressInForgeYAML(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:           "egress-test",
		AgentID:        "egress-test",
		Framework:      "custom",
		Language:       "python",
		ModelProvider:  "openai",
		Channels:       []string{"slack"},
		BuiltinTools:   []string{"web_search"},
		EnvVars:        map[string]string{},
		NonInteractive: true,
	}

	err := scaffold(opts)
	if err != nil {
		t.Fatalf("scaffold error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join("egress-test", "forge.yaml"))
	if err != nil {
		t.Fatalf("reading forge.yaml: %v", err)
	}
	yamlStr := string(content)
	if !strings.Contains(yamlStr, "allowed_domains") {
		t.Error("forge.yaml missing egress allowed_domains section")
	}
	if !strings.Contains(yamlStr, "api.openai.com") {
		t.Error("forge.yaml missing api.openai.com in egress domains")
	}
	if !strings.Contains(yamlStr, "api.tavily.com") {
		t.Error("forge.yaml missing api.tavily.com in egress domains")
	}
}

func TestScaffold_GitignoreIncludesEnv(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:           "gi-test",
		AgentID:        "gi-test",
		Framework:      "custom",
		Language:       "python",
		ModelProvider:  "openai",
		EnvVars:        map[string]string{},
		NonInteractive: true,
	}

	err := scaffold(opts)
	if err != nil {
		t.Fatalf("scaffold error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join("gi-test", ".gitignore"))
	if err != nil {
		t.Fatalf("reading .gitignore: %v", err)
	}
	if !strings.Contains(string(content), ".env") {
		t.Error(".gitignore missing .env entry")
	}
}

func TestDeriveEgressDomains(t *testing.T) {
	opts := &initOptions{
		ModelProvider: "openai",
		Channels:      []string{"slack"},
		BuiltinTools:  []string{"web_search"},
		EnvVars:       map[string]string{},
	}

	skillInfos := lookupSelectedSkills([]string{"github"})
	domains := deriveEgressDomains(opts, skillInfos)

	expected := map[string]bool{
		"api.openai.com":  true,
		"slack.com":       true,
		"hooks.slack.com": true,
		"api.slack.com":   true,
		"api.tavily.com":  true,
		"api.github.com":  true,
		"github.com":      true,
	}
	for _, d := range domains {
		if !expected[d] {
			t.Errorf("unexpected domain: %s", d)
		}
		delete(expected, d)
	}
	for d := range expected {
		t.Errorf("missing expected domain: %s", d)
	}
}

func TestDeriveEgressDomains_Empty(t *testing.T) {
	opts := &initOptions{
		ModelProvider: "ollama",
		EnvVars:       map[string]string{},
	}
	domains := deriveEgressDomains(opts, nil)
	if len(domains) != 0 {
		t.Errorf("expected empty domains for ollama with no tools/channels, got %v", domains)
	}
}

func TestBuildEnvVars(t *testing.T) {
	opts := &initOptions{
		ModelProvider: "openai",
		BuiltinTools:  []string{"web_search"},
		Skills:        []string{"github"},
		EnvVars:       map[string]string{"OPENAI_API_KEY": "sk-test"},
	}
	vars := buildEnvVars(opts)

	found := make(map[string]bool)
	for _, v := range vars {
		found[v.Key] = true
	}
	if !found["OPENAI_API_KEY"] {
		t.Error("missing OPENAI_API_KEY")
	}
	if !found["TAVILY_API_KEY"] {
		t.Error("missing TAVILY_API_KEY")
	}
	if !found["GH_TOKEN"] {
		t.Error("missing GH_TOKEN")
	}
}

func TestContainsStr(t *testing.T) {
	if !containsStr([]string{"a", "b", "c"}, "b") {
		t.Error("expected true for 'b' in [a,b,c]")
	}
	if containsStr([]string{"a", "b", "c"}, "d") {
		t.Error("expected false for 'd' in [a,b,c]")
	}
}

func TestTitleCase(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"openai", "Openai"},
		{"anthropic", "Anthropic"},
		{"", ""},
	}
	for _, tt := range tests {
		got := titleCase(tt.input)
		if got != tt.expected {
			t.Errorf("titleCase(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func assertContainsTemplate(t *testing.T, files []fileToRender, templatePath string) {
	t.Helper()
	for _, f := range files {
		if f.TemplatePath == templatePath {
			return
		}
	}
	t.Errorf("expected file manifest to contain template %q", templatePath)
}

func TestBuildTemplateData_DefaultModels(t *testing.T) {
	tests := []struct {
		provider      string
		expectedModel string
	}{
		{"openai", "gpt-4o-mini"},
		{"anthropic", "claude-sonnet-4-20250514"},
		{"gemini", "gemini-2.5-flash"},
		{"ollama", "llama3"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			opts := &initOptions{
				Name:          "test",
				AgentID:       "test",
				Framework:     "custom",
				Language:      "python",
				ModelProvider: tt.provider,
				EnvVars:       map[string]string{},
			}
			data := buildTemplateData(opts)
			if data.ModelName != tt.expectedModel {
				t.Errorf("model: got %q, want %q", data.ModelName, tt.expectedModel)
			}
		})
	}
}

func TestCollectNonInteractive_GeminiProvider(t *testing.T) {
	opts := &initOptions{
		Name:          "test",
		ModelProvider: "gemini",
		APIKey:        "gem-key",
		EnvVars:       map[string]string{},
	}
	err := collectNonInteractive(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.EnvVars["GEMINI_API_KEY"] != "gem-key" {
		t.Errorf("expected GEMINI_API_KEY=gem-key, got %q", opts.EnvVars["GEMINI_API_KEY"])
	}
}

func TestBuildEnvVars_Gemini(t *testing.T) {
	opts := &initOptions{
		ModelProvider: "gemini",
		EnvVars:       map[string]string{"GEMINI_API_KEY": "gem-test"},
	}
	vars := buildEnvVars(opts)

	found := false
	for _, v := range vars {
		if v.Key == "GEMINI_API_KEY" && v.Value == "gem-test" {
			found = true
		}
	}
	if !found {
		t.Error("missing GEMINI_API_KEY in env vars")
	}
}

func TestScaffold_ForceOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	// Create existing directory
	_ = os.MkdirAll("force-test", 0o755)

	opts := &initOptions{
		Name:           "force-test",
		AgentID:        "force-test",
		Framework:      "custom",
		Language:       "python",
		ModelProvider:  "openai",
		EnvVars:        map[string]string{},
		NonInteractive: true,
		Force:          true,
	}

	err := scaffold(opts)
	if err != nil {
		t.Fatalf("scaffold with --force should succeed: %v", err)
	}
}

func TestScaffold_ExistingDirBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	// Create existing directory
	_ = os.MkdirAll("blocked-test", 0o755)

	opts := &initOptions{
		Name:           "blocked-test",
		AgentID:        "blocked-test",
		Framework:      "custom",
		Language:       "python",
		ModelProvider:  "openai",
		EnvVars:        map[string]string{},
		NonInteractive: true,
		Force:          false,
	}

	err := scaffold(opts)
	if err == nil {
		t.Fatal("expected error when directory exists without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
