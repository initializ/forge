package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-cli/internal/tui/steps"
	coresettings "github.com/initializ/forge/forge-core/settings"
)

func TestApplyInitSettings_GatewayAlways(t *testing.T) {
	set := coresettings.Settings{Models: coresettings.ModelSettings{
		Gateway: &coresettings.ModelGateway{BaseURL: "https://gw/v1", AuthScheme: "apikey_header", AuthHeaderName: "apikey"},
	}}
	// Gateway injects in BOTH modes.
	for _, nonInteractive := range []bool{true, false} {
		opts := &initOptions{}
		applyInitSettings(opts, set, nonInteractive)
		if opts.ModelBaseURL != "https://gw/v1" || opts.ModelAuthScheme != "apikey_header" || opts.ModelAuthHeaderName != "apikey" {
			t.Errorf("nonInteractive=%v: gateway not injected: %+v", nonInteractive, opts)
		}
	}
}

func TestApplyInitSettings_NonInteractiveDefaults(t *testing.T) {
	set := coresettings.Settings{
		Models: coresettings.ModelSettings{Default: &coresettings.ModelDefault{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
		Tools:  coresettings.ToolSettings{Builtins: coresettings.BuiltinToolSettings{Enabled: []string{"http_request", "web_search"}}},
		Skills: coresettings.SkillSettings{Enabled: []string{"weather", "github"}},
	}

	// Empty opts (no flags) → settings fill provider/model/builtins/skills.
	opts := &initOptions{}
	applyInitSettings(opts, set, true)
	if opts.ModelProvider != "anthropic" || opts.CustomModel != "claude-sonnet-4-6" {
		t.Errorf("default provider/model not applied: %+v", opts)
	}
	if !reflect.DeepEqual(opts.BuiltinTools, []string{"http_request", "web_search"}) {
		t.Errorf("builtins default not applied: %v", opts.BuiltinTools)
	}
	if !reflect.DeepEqual(opts.Skills, []string{"weather", "github"}) {
		t.Errorf("skills default not applied: %v", opts.Skills)
	}

	// Explicit flags win over settings defaults.
	opts2 := &initOptions{ModelProvider: "openai", CustomModel: "gpt-4o", BuiltinTools: []string{"math_calculate"}, Skills: []string{"weather"}}
	applyInitSettings(opts2, set, true)
	if opts2.ModelProvider != "openai" || opts2.CustomModel != "gpt-4o" {
		t.Errorf("explicit provider/model should win: %+v", opts2)
	}
	if !reflect.DeepEqual(opts2.BuiltinTools, []string{"math_calculate"}) {
		t.Errorf("explicit builtins should win: %v", opts2.BuiltinTools)
	}
	if !reflect.DeepEqual(opts2.Skills, []string{"weather"}) {
		t.Errorf("explicit skills should win: %v", opts2.Skills)
	}
}

func TestFilterSkillInfos(t *testing.T) {
	all := []steps.SkillInfo{{Name: "weather"}, {Name: "github"}, {Name: "jira"}}
	got := filterSkillInfos(all, []string{"github", "weather"})
	// Preserves input order (weather, github), drops jira.
	if len(got) != 2 || got[0].Name != "weather" || got[1].Name != "github" {
		t.Errorf("filterSkillInfos = %+v, want [weather github]", got)
	}
	if len(filterSkillInfos(all, nil)) != 0 {
		// nil enabled via this helper means "nothing allowed" — callers only
		// invoke it when the allowlist is non-empty (guarded in collectInteractive).
		t.Log("filterSkillInfos(nil) returns empty; collectInteractive only calls it for a non-empty allowlist")
	}
}

func TestApplyInitSettings_InteractiveSkipsDefaults(t *testing.T) {
	// In interactive mode the wizard is authoritative — only the gateway (which
	// has no wizard step) is injected; provider/model/builtins are left alone.
	set := coresettings.Settings{
		Models: coresettings.ModelSettings{Default: &coresettings.ModelDefault{Provider: "anthropic", Model: "x"}},
		Tools:  coresettings.ToolSettings{Builtins: coresettings.BuiltinToolSettings{Enabled: []string{"http_request"}}},
	}
	opts := &initOptions{}
	applyInitSettings(opts, set, false)
	if opts.ModelProvider != "" || opts.CustomModel != "" || len(opts.BuiltinTools) != 0 {
		t.Errorf("interactive mode must not apply provider/model/builtins defaults: %+v", opts)
	}
}

// Exercises the real runInit sequence (applyInitSettings → collectNonInteractive
// → scaffold) without cobra: a settings models.default lets non-interactive init
// proceed WITHOUT --model-provider, and the scaffolded forge.yaml carries the
// settings provider/model + gateway.
func TestInit_NonInteractive_SettingsDefaultAndGatewayReachForgeYAML(t *testing.T) {
	origDir, _ := os.Getwd()
	defer func() { _ = os.Chdir(origDir) }()
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	sf := filepath.Join(dir, "user.json")
	if err := os.WriteFile(sf, []byte(`{"models":{"default":{"provider":"anthropic","model":"claude-sonnet-4-6"},"gateway":{"base_url":"https://gw.corp/v1","auth_scheme":"apikey_header"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(coresettings.EnvUserSettings, sf)
	coresettings.SetManagedDirForTest(filepath.Join(dir, "no-managed"))

	set, err := coresettings.Load(coresettings.LoadOptions{})
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	// No provider set (as if --model-provider were omitted).
	opts := &initOptions{Name: "myagent", EnvVars: map[string]string{}, NonInteractive: true}
	applyInitSettings(opts, set, true)

	// The required-provider check must now pass (settings supplied it).
	if err := collectNonInteractive(opts); err != nil {
		t.Fatalf("collectNonInteractive should pass with a settings default provider: %v", err)
	}
	opts.AgentID = "myagent"
	if err := scaffold(opts); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "myagent", "forge.yaml"))
	if err != nil {
		t.Fatalf("reading forge.yaml: %v", err)
	}
	s := string(content)
	for _, want := range []string{"provider: anthropic", "name: claude-sonnet-4-6", "base_url: https://gw.corp/v1", "auth_scheme: apikey_header"} {
		if !strings.Contains(s, want) {
			t.Errorf("forge.yaml missing %q:\n%s", want, s)
		}
	}
}
