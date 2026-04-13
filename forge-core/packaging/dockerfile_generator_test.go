package packaging

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

func TestGenerateDockerfile_NoBins(t *testing.T) {
	content, warnings, err := GenerateDockerfile(nil, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestGenerateDockerfile_AptBatch(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
			{Name: "curl"},
		},
	}

	content, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "FROM debian:bookworm-slim") {
		t.Error("expected debian:bookworm-slim base image")
	}
	if !strings.Contains(content, "apt-get install") {
		t.Error("expected apt-get install")
	}
	if !strings.Contains(content, "jq") {
		t.Error("expected jq package")
	}
	if !strings.Contains(content, "curl") {
		t.Error("expected curl package")
	}
}

func TestGenerateDockerfile_Alpine(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
		},
	}

	content, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "FROM alpine:3.20") {
		t.Error("expected alpine:3.20 base image")
	}
	if !strings.Contains(content, "apk add") {
		t.Error("expected apk add")
	}
}

func TestGenerateDockerfile_DirectURL(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "kubectl"},
		},
	}

	content, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "curl -fsSL") {
		t.Error("expected curl download command for kubectl")
	}
}

func TestGenerateDockerfile_CustomBaseImage(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
		},
	}

	cfg := types.PackageConfig{BaseImage: "ubuntu:24.04"}
	content, _, err := GenerateDockerfile(manifest, cfg, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "FROM ubuntu:24.04") {
		t.Error("expected custom base image ubuntu:24.04")
	}
}

func TestGenerateDockerfile_Heavy(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "playwright"},
		},
	}

	content, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "FROM mcr.microsoft.com/playwright") {
		t.Error("expected playwright companion image stage")
	}
	if !strings.Contains(content, "COPY --from=bin-playwright") {
		t.Error("expected COPY from companion stage")
	}
}

func TestGenerateDockerfile_LocalFile(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "forge"},
		},
	}

	cfg := types.PackageConfig{
		BinOverrides: map[string]types.BinOverride{
			"forge": {LocalPath: "/usr/local/bin/forge"},
		},
	}

	content, _, err := GenerateDockerfile(manifest, cfg, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "COPY .local-bins/forge /usr/local/bin/forge") {
		t.Errorf("expected COPY .local-bins/forge instruction, got:\n%s", content)
	}
	if !strings.Contains(content, "RUN chmod 0755 /usr/local/bin/forge") {
		t.Errorf("expected chmod instruction, got:\n%s", content)
	}
}

func TestGenerateDockerfile_AlpineBlockedByUbuntu(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "playwright"},
			{Name: "jq"},
		},
	}

	content, warnings, err := GenerateDockerfile(manifest, types.PackageConfig{}, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(content, "FROM debian:bookworm-slim") {
		t.Error("expected fallback to debian when alpine blocked")
	}

	hasAlpineWarning := false
	for _, w := range warnings {
		if strings.Contains(w, "Alpine") {
			hasAlpineWarning = true
		}
	}
	if !hasAlpineWarning {
		t.Error("expected warning about alpine being blocked")
	}
}
