package local

import (
	"testing"
	"testing/fstest"
)

func TestLocalRegistry_Basic(t *testing.T) {
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
	}

	reg, err := NewLocalRegistry(fsys)
	if err != nil {
		t.Fatalf("NewLocalRegistry error: %v", err)
	}

	// List
	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	// Get
	sd := reg.Get("github")
	if sd == nil {
		t.Fatal("Get(\"github\") returned nil")
	}
	if sd.Description != "GitHub integration" {
		t.Errorf("Description = %q", sd.Description)
	}

	// Get nonexistent
	if reg.Get("nonexistent") != nil {
		t.Error("Get(\"nonexistent\") should return nil")
	}

	// LoadContent
	content, err := reg.LoadContent("github")
	if err != nil {
		t.Fatalf("LoadContent error: %v", err)
	}
	if len(content) == 0 {
		t.Error("LoadContent returned empty content")
	}

	// HasScript / LoadScript
	if reg.HasScript("github") {
		t.Error("github should not have a script")
	}
}

func TestLocalRegistry_WithScript(t *testing.T) {
	fsys := fstest.MapFS{
		"tavily-search/SKILL.md": &fstest.MapFile{
			Data: []byte(`---
name: tavily-search
description: Web search
metadata:
  forge:
    requires:
      bins:
        - curl
        - jq
      env:
        required:
          - TAVILY_API_KEY
    egress_domains:
      - api.tavily.com
---
## Tool: tavily_search
Search.
`),
		},
		"tavily-search/scripts/tavily-search.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho hello\n"),
		},
	}

	reg, err := NewLocalRegistry(fsys)
	if err != nil {
		t.Fatalf("NewLocalRegistry error: %v", err)
	}

	if !reg.HasScript("tavily-search") {
		t.Error("tavily-search should have a script")
	}

	script, err := reg.LoadScript("tavily-search")
	if err != nil {
		t.Fatalf("LoadScript error: %v", err)
	}
	if len(script) == 0 {
		t.Error("LoadScript returned empty content")
	}
}
