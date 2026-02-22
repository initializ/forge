package custom

import "testing"

func TestPlugin_Name(t *testing.T) {
	p := &Plugin{}
	if p.Name() != "custom" {
		t.Errorf("Name() = %q, want custom", p.Name())
	}
}

func TestPlugin_DetectProject_AlwaysTrue(t *testing.T) {
	p := &Plugin{}
	ok, err := p.DetectProject(t.TempDir())
	if err != nil {
		t.Fatalf("DetectProject error: %v", err)
	}
	if !ok {
		t.Error("DetectProject should always return true")
	}
}

func TestPlugin_ExtractAgentConfig_Empty(t *testing.T) {
	p := &Plugin{}
	cfg, err := p.ExtractAgentConfig(t.TempDir())
	if err != nil {
		t.Fatalf("ExtractAgentConfig error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil AgentConfig")
	}
	if cfg.Name != "" {
		t.Errorf("expected empty Name, got %q", cfg.Name)
	}
}

func TestPlugin_GenerateWrapper_Nil(t *testing.T) {
	p := &Plugin{}
	data, err := p.GenerateWrapper(nil)
	if err != nil {
		t.Fatalf("GenerateWrapper error: %v", err)
	}
	if data != nil {
		t.Error("expected nil wrapper for custom plugin")
	}
}

func TestPlugin_RuntimeDependencies_Nil(t *testing.T) {
	p := &Plugin{}
	deps := p.RuntimeDependencies()
	if deps != nil {
		t.Errorf("expected nil dependencies, got %v", deps)
	}
}
