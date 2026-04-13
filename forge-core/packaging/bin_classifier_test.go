package packaging

import (
	"testing"

	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

func TestClassify_SimpleApt(t *testing.T) {
	c, err := NewBinClassifier(types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
			{Name: "curl"},
		},
	}

	resolutions, warnings, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(resolutions) != 2 {
		t.Fatalf("expected 2 resolutions, got %d", len(resolutions))
	}

	for _, r := range resolutions {
		if r.Method != MethodApt {
			t.Errorf("%s: method = %v, want apt", r.Name, r.Method)
		}
	}
	_ = warnings
}

func TestClassify_Alpine(t *testing.T) {
	c, err := NewBinClassifier(types.PackageConfig{}, false, true)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(resolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(resolutions))
	}
	if resolutions[0].Method != MethodApk {
		t.Errorf("method = %v, want apk", resolutions[0].Method)
	}
	if resolutions[0].Package != "jq" {
		t.Errorf("package = %q, want jq", resolutions[0].Package)
	}
}

func TestClassify_RegistryURL(t *testing.T) {
	c, err := NewBinClassifier(types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "kubectl"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(resolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(resolutions))
	}
	// kubectl has a URL in registry and custom run is not set, so should use direct-url
	r := resolutions[0]
	if r.Method != MethodDirectURL {
		t.Errorf("method = %v, want direct-url", r.Method)
	}
	if r.URL == "" {
		t.Error("URL should not be empty for kubectl")
	}
}

func TestClassify_SkillOverride(t *testing.T) {
	c, err := NewBinClassifier(types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "custom-bin", AptPackage: "custom-pkg"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if resolutions[0].Method != MethodApt {
		t.Errorf("method = %v, want apt", resolutions[0].Method)
	}
	if resolutions[0].Package != "custom-pkg" {
		t.Errorf("package = %q, want custom-pkg", resolutions[0].Package)
	}
}

func TestClassify_ConfigOverride(t *testing.T) {
	cfg := types.PackageConfig{
		BinOverrides: map[string]types.BinOverride{
			"jq": {AptPackage: "jq-special"},
		},
	}
	c, err := NewBinClassifier(cfg, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if resolutions[0].Package != "jq-special" {
		t.Errorf("package = %q, want jq-special (config override)", resolutions[0].Package)
	}
}

func TestClassify_LocalFile(t *testing.T) {
	cfg := types.PackageConfig{
		BinOverrides: map[string]types.BinOverride{
			"forge": {LocalPath: "/usr/local/bin/forge"},
		},
	}
	c, err := NewBinClassifier(cfg, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "forge"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(resolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(resolutions))
	}
	r := resolutions[0]
	if r.Method != MethodLocalFile {
		t.Errorf("method = %v, want local-file", r.Method)
	}
	if r.LocalPath != "/usr/local/bin/forge" {
		t.Errorf("local path = %q, want /usr/local/bin/forge", r.LocalPath)
	}
	if r.Dest != "/usr/local/bin/forge" {
		t.Errorf("dest = %q, want /usr/local/bin/forge", r.Dest)
	}
	if r.Chmod != "0755" {
		t.Errorf("chmod = %q, want 0755", r.Chmod)
	}
}

func TestClassify_LocalFileOverridesSkill(t *testing.T) {
	cfg := types.PackageConfig{
		BinOverrides: map[string]types.BinOverride{
			"mybin": {LocalPath: "/tmp/mybin", Dest: "/opt/bin/mybin"},
		},
	}
	c, err := NewBinClassifier(cfg, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	// Even though skill sets AptPackage, local file override should win
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "mybin", AptPackage: "mybin-pkg"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if resolutions[0].Method != MethodLocalFile {
		t.Errorf("method = %v, want local-file (should override skill)", resolutions[0].Method)
	}
	if resolutions[0].Dest != "/opt/bin/mybin" {
		t.Errorf("dest = %q, want /opt/bin/mybin", resolutions[0].Dest)
	}
}

func TestClassify_Unknown(t *testing.T) {
	c, err := NewBinClassifier(types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "unknown-thing-xyz"},
		},
	}

	resolutions, warnings, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(warnings) == 0 {
		t.Error("expected warning for unknown binary")
	}
	if resolutions[0].Method != MethodApt {
		t.Errorf("method = %v, want apt (best-effort)", resolutions[0].Method)
	}
}

func TestClassify_Dedup(t *testing.T) {
	c, err := NewBinClassifier(types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
			{Name: "jq"},
			{Name: "curl"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(resolutions) != 2 {
		t.Errorf("expected 2 resolutions (deduped), got %d", len(resolutions))
	}
}

func TestClassify_Heavy(t *testing.T) {
	c, err := NewBinClassifier(types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("NewBinClassifier: %v", err)
	}

	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "playwright"},
		},
	}

	resolutions, _, err := c.Classify(manifest)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if resolutions[0].Method != MethodImageCopy {
		t.Errorf("method = %v, want image-copy", resolutions[0].Method)
	}
	if resolutions[0].Image == "" {
		t.Error("image should not be empty for heavy binary")
	}
}

func TestTopoSort_Basic(t *testing.T) {
	resolutions := []BinResolution{
		{Name: "aws", RequiresFirst: []string{"unzip"}},
		{Name: "unzip"},
	}

	sorted, err := topoSort(resolutions)
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}

	if sorted[0].Name != "unzip" {
		t.Errorf("expected unzip first, got %s", sorted[0].Name)
	}
	if sorted[1].Name != "aws" {
		t.Errorf("expected aws second, got %s", sorted[1].Name)
	}
}

func TestTopoSort_NoDeps(t *testing.T) {
	resolutions := []BinResolution{
		{Name: "jq"},
		{Name: "curl"},
	}

	sorted, err := topoSort(resolutions)
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}

	if len(sorted) != 2 {
		t.Errorf("expected 2 items, got %d", len(sorted))
	}
}
