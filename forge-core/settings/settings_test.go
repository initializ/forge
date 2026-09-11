package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolve_ManagedWinsScalar(t *testing.T) {
	layers := []Layer{
		{Source: LayerUser, Settings: Settings{Models: ModelSettings{Default: &ModelDefault{Provider: "openai", Model: "gpt-4o"}}}},
		{Source: LayerManaged, Settings: Settings{Models: ModelSettings{Default: &ModelDefault{Provider: "anthropic", Model: "claude-sonnet-4-6"}}}},
	}
	got := Resolve(layers)
	if got.Models.Default.Provider != "anthropic" || got.Models.Default.Model != "claude-sonnet-4-6" {
		t.Errorf("managed default should win, got %+v", got.Models.Default)
	}
}

func TestResolve_ScalarFieldMergeKeepsLowerWhenHigherEmpty(t *testing.T) {
	// user sets model; managed sets only provider → provider from managed,
	// model preserved from user.
	layers := []Layer{
		{Source: LayerUser, Settings: Settings{Models: ModelSettings{Default: &ModelDefault{Provider: "openai", Model: "gpt-4o"}}}},
		{Source: LayerManaged, Settings: Settings{Models: ModelSettings{Default: &ModelDefault{Provider: "anthropic"}}}},
	}
	got := Resolve(layers)
	if got.Models.Default.Provider != "anthropic" || got.Models.Default.Model != "gpt-4o" {
		t.Errorf("expected provider=anthropic (managed) + model=gpt-4o (user), got %+v", got.Models.Default)
	}
}

func TestResolve_ListsUnion(t *testing.T) {
	layers := []Layer{
		{Source: LayerUser, Settings: Settings{Channels: ChannelSettings{Enabled: []string{"slack"}}}},
		{Source: LayerProject, Settings: Settings{Channels: ChannelSettings{Enabled: []string{"telegram", "slack"}}}},
	}
	got := Resolve(layers).Channels.Enabled
	want := []string{"slack", "telegram"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("channels union = %v, want %v (deduped, stable)", got, want)
	}
}

func TestResolve_ManagedAvailableModelsLock(t *testing.T) {
	// A managed allowlist is authoritative: a lower layer cannot WIDEN it.
	layers := []Layer{
		{Source: LayerUser, Settings: Settings{Models: ModelSettings{AvailableModels: []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-6"}}}},
		{Source: LayerManaged, ManagedLock: true, Settings: Settings{Models: ModelSettings{AvailableModels: []string{"anthropic/claude-sonnet-4-6"}}}},
	}
	got := Resolve(layers).Models.AvailableModels
	want := []string{"anthropic/claude-sonnet-4-6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("managed allowlist must LOCK (replace, not union): got %v, want %v", got, want)
	}
}

func TestResolve_AvailableModelsUnionWhenNoManagedLock(t *testing.T) {
	// Without a managed lock, allowlist entries merge across layers.
	layers := []Layer{
		{Source: LayerUser, Settings: Settings{Models: ModelSettings{AvailableModels: []string{"openai/gpt-4o"}}}},
		{Source: LayerProject, Settings: Settings{Models: ModelSettings{AvailableModels: []string{"anthropic/claude-sonnet-4-6"}}}},
	}
	got := Resolve(layers).Models.AvailableModels
	want := []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("non-managed allowlist should union: got %v, want %v", got, want)
	}
}

func TestResolve_GatewayFieldMerge(t *testing.T) {
	layers := []Layer{
		{Source: LayerUser, Settings: Settings{Models: ModelSettings{Gateway: &ModelGateway{BaseURL: "https://user.example", AuthScheme: "bearer"}}}},
		{Source: LayerManaged, Settings: Settings{Models: ModelSettings{Gateway: &ModelGateway{BaseURL: "https://gw.example", AuthHeaderName: "apikey"}}}},
	}
	gw := Resolve(layers).Models.Gateway
	if gw.BaseURL != "https://gw.example" {
		t.Errorf("base_url = %q, want managed https://gw.example", gw.BaseURL)
	}
	if gw.AuthScheme != "bearer" {
		t.Errorf("auth_scheme = %q, want user bearer (managed didn't set it)", gw.AuthScheme)
	}
	if gw.AuthHeaderName != "apikey" {
		t.Errorf("auth_header_name = %q, want managed apikey", gw.AuthHeaderName)
	}
}

func TestLoadAllLayers_DiscoveryPrecedenceAndResolve(t *testing.T) {
	dir := t.TempDir()
	userFile := filepath.Join(dir, "user-settings.json")
	managedFile := filepath.Join(dir, "managed", "managed-settings.json")
	wd := filepath.Join(dir, "project")

	writeJSON(t, userFile, `{"models":{"default":{"provider":"openai","model":"gpt-4o"}},"channels":{"enabled":["slack"]}}`)
	writeJSON(t, filepath.Join(wd, ".forge", "settings.json"), `{"channels":{"enabled":["telegram"]}}`)
	writeJSON(t, managedFile, `{"models":{"default":{"provider":"anthropic","model":"claude-sonnet-4-6"},"available_models":["anthropic/claude-sonnet-4-6"]}}`)

	t.Setenv(EnvUserSettings, userFile)
	t.Setenv(EnvManagedSettings, managedFile)

	layers, err := LoadAllLayers(LoadOptions{WorkingDir: wd})
	if err != nil {
		t.Fatalf("LoadAllLayers: %v", err)
	}
	// Order: user, project, managed (project-local + cli absent).
	var order []string
	for _, l := range layers {
		order = append(order, l.Source)
	}
	if !reflect.DeepEqual(order, []string{LayerUser, LayerProject, LayerManaged}) {
		t.Errorf("layer order = %v, want [user project managed]", order)
	}

	got := Resolve(layers)
	if got.Models.Default.Provider != "anthropic" {
		t.Errorf("managed default should win: %+v", got.Models.Default)
	}
	if !reflect.DeepEqual(got.Channels.Enabled, []string{"slack", "telegram"}) {
		t.Errorf("channels union = %v, want [slack telegram]", got.Channels.Enabled)
	}
	if !reflect.DeepEqual(got.Models.AvailableModels, []string{"anthropic/claude-sonnet-4-6"}) {
		t.Errorf("managed allowlist lock = %v", got.Models.AvailableModels)
	}
}

func TestManagedEmptyAvailableModels_IsUnsetNotLock(t *testing.T) {
	// An empty (or absent) managed available_models must NOT lock to zero —
	// it's "unset", so a lower layer's entries survive. []string can't
	// distinguish JSON [] from absent, and lock-to-zero is degenerate.
	dir := t.TempDir()
	managedFile := filepath.Join(dir, "managed-settings.json")
	writeJSON(t, managedFile, `{"models":{"available_models":[]}}`)
	userFile := filepath.Join(dir, "user.json")
	writeJSON(t, userFile, `{"models":{"available_models":["openai/gpt-4o"]}}`)
	t.Setenv(EnvManagedSettings, managedFile)
	t.Setenv(EnvUserSettings, userFile)

	layers, err := LoadAllLayers(LoadOptions{WorkingDir: filepath.Join(dir, "empty")})
	if err != nil {
		t.Fatalf("LoadAllLayers: %v", err)
	}
	for _, l := range layers {
		if l.Source == LayerManaged && l.ManagedLock {
			t.Error("empty managed available_models must NOT set ManagedLock")
		}
	}
	got := Resolve(layers).Models.AvailableModels
	if !reflect.DeepEqual(got, []string{"openai/gpt-4o"}) {
		t.Errorf("empty managed list should leave the user entry: got %v", got)
	}
}

func TestLoadManagedDropins_MergedAlphabetically(t *testing.T) {
	dir := t.TempDir()
	managedFile := filepath.Join(dir, "managed-settings.json")
	writeJSON(t, managedFile, `{"channels":{"enabled":["slack"]}}`)
	writeJSON(t, filepath.Join(dir, "managed-settings.d", "20-tools.json"), `{"tools":{"builtins":{"enabled":["http_request"]}}}`)
	writeJSON(t, filepath.Join(dir, "managed-settings.d", "10-channels.json"), `{"channels":{"enabled":["telegram"]}}`)
	t.Setenv(EnvManagedSettings, managedFile)
	// Neutralize the real user/home layer so the test is hermetic.
	t.Setenv(EnvUserSettings, filepath.Join(dir, "no-such-user.json"))

	layers, err := LoadAllLayers(LoadOptions{WorkingDir: filepath.Join(dir, "empty")})
	if err != nil {
		t.Fatalf("LoadAllLayers: %v", err)
	}
	got := Resolve(layers)
	// primary(slack) + 10-channels(telegram) union; 20-tools adds http_request.
	if !reflect.DeepEqual(got.Channels.Enabled, []string{"slack", "telegram"}) {
		t.Errorf("dropin channels = %v, want [slack telegram]", got.Channels.Enabled)
	}
	if !reflect.DeepEqual(got.Tools.Builtins.Enabled, []string{"http_request"}) {
		t.Errorf("dropin tools = %v, want [http_request]", got.Tools.Builtins.Enabled)
	}
}

func TestLoadFile_ErrorsAndOmissions(t *testing.T) {
	dir := t.TempDir()

	// Absent → omitted, no error.
	if _, present, err := loadFile(filepath.Join(dir, "nope.json")); err != nil || present {
		t.Errorf("absent file: present=%v err=%v, want false,nil", present, err)
	}
	// Empty → omitted.
	empty := filepath.Join(dir, "empty.json")
	writeJSON(t, empty, "  \n")
	if _, present, err := loadFile(empty); err != nil || present {
		t.Errorf("empty file: present=%v err=%v, want false,nil", present, err)
	}
	// Malformed → hard error.
	bad := filepath.Join(dir, "bad.json")
	writeJSON(t, bad, `{not json`)
	if _, _, err := loadFile(bad); err == nil {
		t.Error("malformed file must error")
	}
	// Unknown field → hard error (typo protection).
	typo := filepath.Join(dir, "typo.json")
	writeJSON(t, typo, `{"modelz":{}}`)
	if _, _, err := loadFile(typo); err == nil {
		t.Error("unknown field must error")
	}
}
