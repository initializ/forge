package local

import (
	"testing"
	"testing/fstest"
)

func TestScan_ValidSkills(t *testing.T) {
	fsys := fstest.MapFS{
		"github/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: github
description: GitHub integration
metadata:
  forge:
    requires:
      bins:
        - gh
      env:
        required:
          - GH_TOKEN
    egress_domains:
      - api.github.com
---
## Tool: github_create_issue
Create issues.
`),
		},
		"weather/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: weather
description: Weather data
metadata:
  forge:
    requires:
      bins:
        - curl
    egress_domains:
      - api.openweathermap.org
---
## Tool: weather_current
Get current weather.
`),
		},
	}

	skills, err := Scan(fsys)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	// Find github
	var github *struct {
		name, desc string
		bins, env  []string
		egress     []string
	}
	for _, s := range skills {
		if s.Name == "github" {
			github = &struct {
				name, desc string
				bins, env  []string
				egress     []string
			}{s.Name, s.Description, s.RequiredBins, s.RequiredEnv, s.EgressDomains}
		}
	}
	if github == nil {
		t.Fatal("github skill not found")
	}
	if github.desc != "GitHub integration" {
		t.Errorf("github description = %q", github.desc)
	}
	if len(github.bins) != 1 || github.bins[0] != "gh" {
		t.Errorf("github bins = %v", github.bins)
	}
	if len(github.env) != 1 || github.env[0] != "GH_TOKEN" {
		t.Errorf("github env = %v", github.env)
	}
	if len(github.egress) != 1 || github.egress[0] != "api.github.com" {
		t.Errorf("github egress = %v", github.egress)
	}
}

func TestScan_SkipsHiddenAndTemplate(t *testing.T) {
	fsys := fstest.MapFS{
		".hidden/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: hidden
---
## Tool: hidden_tool
Hidden.
`),
		},
		"_template/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: template
---
## Tool: template_tool
Template.
`),
		},
		"real/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: real
description: A real skill
---
## Tool: real_tool
Real.
`),
		},
	}

	skills, err := Scan(fsys)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "real" {
		t.Errorf("expected 'real', got %q", skills[0].Name)
	}
}

func TestScan_SkipsDirsWithoutSkillMD(t *testing.T) {
	fsys := fstest.MapFS{
		"noskill/README.md": &fstest.MapFile{
			Data: []byte("no skill here"),
		},
		"valid/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: valid
description: Valid skill
---
## Tool: valid_tool
Valid.
`),
		},
	}

	skills, err := Scan(fsys)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "valid" {
		t.Errorf("expected 'valid', got %q", skills[0].Name)
	}
}

func TestScan_CategoryAndTagsPropagated(t *testing.T) {
	fsys := fstest.MapFS{
		"k8s-triage/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: k8s-triage
description: Kubernetes incident triage
category: sre
tags:
  - kubernetes
  - incident-response
  - triage
metadata:
  forge:
    requires:
      bins:
        - kubectl
---
## Tool: k8s_triage
Diagnose Kubernetes workloads.
`),
		},
	}

	skills, err := Scan(fsys)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Category != "sre" {
		t.Errorf("Category = %q, want sre", skills[0].Category)
	}
	wantTags := []string{"kubernetes", "incident-response", "triage"}
	if len(skills[0].Tags) != len(wantTags) {
		t.Fatalf("Tags = %v, want %v", skills[0].Tags, wantTags)
	}
	for i, tag := range wantTags {
		if skills[0].Tags[i] != tag {
			t.Errorf("Tags[%d] = %q, want %q", i, skills[0].Tags[i], tag)
		}
	}
}

func TestScan_DeniedToolsPropagated(t *testing.T) {
	fsys := fstest.MapFS{
		"k8s-triage/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: k8s-triage
description: Kubernetes triage
metadata:
  forge:
    requires:
      bins:
        - kubectl
    denied_tools:
      - http_request
      - web_search
---
## Tool: k8s_triage
Diagnose workloads.
`),
		},
	}

	skills, err := Scan(fsys)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if len(skills[0].DeniedTools) != 2 {
		t.Fatalf("DeniedTools = %v, want 2 items", skills[0].DeniedTools)
	}
	if skills[0].DeniedTools[0] != "http_request" {
		t.Errorf("DeniedTools[0] = %q, want http_request", skills[0].DeniedTools[0])
	}
	if skills[0].DeniedTools[1] != "web_search" {
		t.Errorf("DeniedTools[1] = %q, want web_search", skills[0].DeniedTools[1])
	}
}

func TestScan_DisplayNameDerived(t *testing.T) {
	fsys := fstest.MapFS{
		"tavily-search/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: tavily-search
description: Web search
---
## Tool: tavily_search
Search.
`),
		},
	}

	skills, err := Scan(fsys)
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].DisplayName != "Tavily Search" {
		t.Errorf("DisplayName = %q, want 'Tavily Search'", skills[0].DisplayName)
	}
}
