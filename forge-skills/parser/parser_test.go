package parser

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse_HeadingFormat(t *testing.T) {
	input := `# My Agent Skills

## Tool: web_search

A tool for searching the web.

**Input:** query string
**Output:** list of results

## Tool: sql_query

Run SQL queries against the database.
`
	entries, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Name != "web_search" {
		t.Errorf("entry[0].Name = %q, want web_search", entries[0].Name)
	}
	if entries[0].Description != "A tool for searching the web." {
		t.Errorf("entry[0].Description = %q", entries[0].Description)
	}
	if entries[0].InputSpec != "query string" {
		t.Errorf("entry[0].InputSpec = %q, want 'query string'", entries[0].InputSpec)
	}
	if entries[0].OutputSpec != "list of results" {
		t.Errorf("entry[0].OutputSpec = %q, want 'list of results'", entries[0].OutputSpec)
	}

	if entries[1].Name != "sql_query" {
		t.Errorf("entry[1].Name = %q, want sql_query", entries[1].Name)
	}
	if entries[1].Description != "Run SQL queries against the database." {
		t.Errorf("entry[1].Description = %q", entries[1].Description)
	}
}

func TestParse_LegacyListItems(t *testing.T) {
	input := `# Tools

- calculator
- translator
- this is a sentence and should be ignored
`
	entries, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Name != "calculator" {
		t.Errorf("entry[0].Name = %q, want calculator", entries[0].Name)
	}
	if entries[1].Name != "translator" {
		t.Errorf("entry[1].Name = %q, want translator", entries[1].Name)
	}
}

func TestParse_Mixed(t *testing.T) {
	input := `# Skills

## Tool: api_client

Calls external APIs.

- helper_util
`
	entries, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "api_client" {
		t.Errorf("entry[0].Name = %q, want api_client", entries[0].Name)
	}
}

func TestParse_MixedOutsideEntry(t *testing.T) {
	input := `# Skills

## Tool: api_client

Calls external APIs.

# Other section

- helper_util
`
	entries, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Name != "api_client" {
		t.Errorf("entry[0].Name = %q, want api_client", entries[0].Name)
	}
	if entries[1].Name != "helper_util" {
		t.Errorf("entry[1].Name = %q, want helper_util", entries[1].Name)
	}
}

func TestParse_Empty(t *testing.T) {
	entries, err := Parse(strings.NewReader(``))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestParse_MultilineDescription(t *testing.T) {
	input := `## Tool: complex_tool

This tool does many things.
It has a long description
spanning multiple lines.

**Input:** JSON payload
**Output:** processed result
`
	entries, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := "This tool does many things. It has a long description spanning multiple lines."
	if entries[0].Description != want {
		t.Errorf("Description = %q, want %q", entries[0].Description, want)
	}
}

func TestParse_OutputFormat(t *testing.T) {
	t.Run("parsed into OutputFormat field", func(t *testing.T) {
		input := `## Tool: web_search
Search the web.

**Input:** query string
**Output:** list of results
**Output format:** Use markdown tables for results. Wrap URLs in code blocks.
`
		entries, err := Parse(strings.NewReader(input))
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].OutputFormat != "Use markdown tables for results. Wrap URLs in code blocks." {
			t.Errorf("OutputFormat = %q", entries[0].OutputFormat)
		}
	})

	t.Run("coexists with Input and Output", func(t *testing.T) {
		input := `## Tool: formatter
Format data.

**Input:** raw data
**Output:** formatted output
**Output format:** Always use code blocks.
`
		entries, err := Parse(strings.NewReader(input))
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].InputSpec != "raw data" {
			t.Errorf("InputSpec = %q, want 'raw data'", entries[0].InputSpec)
		}
		if entries[0].OutputSpec != "formatted output" {
			t.Errorf("OutputSpec = %q, want 'formatted output'", entries[0].OutputSpec)
		}
		if entries[0].OutputFormat != "Always use code blocks." {
			t.Errorf("OutputFormat = %q, want 'Always use code blocks.'", entries[0].OutputFormat)
		}
	})

	t.Run("missing leaves field empty", func(t *testing.T) {
		input := `## Tool: simple
A simple tool.

**Input:** text
**Output:** result
`
		entries, err := Parse(strings.NewReader(input))
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].OutputFormat != "" {
			t.Errorf("OutputFormat should be empty, got %q", entries[0].OutputFormat)
		}
	})
}

func TestParseWithMetadata_NoFrontmatter(t *testing.T) {
	input := `## Tool: web_search
A tool for searching the web.

**Input:** query string
**Output:** list of results
`
	entries, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta != nil {
		t.Error("expected nil metadata for no frontmatter")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "web_search" {
		t.Errorf("entry[0].Name = %q, want web_search", entries[0].Name)
	}
	if entries[0].Metadata != nil {
		t.Error("expected nil Metadata on entry")
	}
	if entries[0].ForgeReqs != nil {
		t.Error("expected nil ForgeReqs on entry")
	}
}

func TestParseWithMetadata_WithForgeRequires(t *testing.T) {
	input := `---
name: github
description: GitHub integration
metadata:
  forge:
    requires:
      bins:
        - curl
        - jq
      env:
        required:
          - API_KEY
        one_of:
          - OPENAI_API_KEY
          - ANTHROPIC_API_KEY
        optional:
          - FIRECRAWL_API_KEY
---
## Tool: github_create_issue
Create a GitHub issue.

**Input:** url: string
**Output:** issue_url: string
`
	entries, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata")
	}
	if meta.Name != "github" {
		t.Errorf("meta.Name = %q, want github", meta.Name)
	}
	if meta.Description != "GitHub integration" {
		t.Errorf("meta.Description = %q", meta.Description)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ForgeReqs == nil {
		t.Fatal("expected non-nil ForgeReqs")
	}
	if !reflect.DeepEqual(entries[0].ForgeReqs.Bins, []string{"curl", "jq"}) {
		t.Errorf("Bins = %v, want [curl jq]", entries[0].ForgeReqs.Bins)
	}
	if entries[0].ForgeReqs.Env == nil {
		t.Fatal("expected non-nil Env")
	}
	if !reflect.DeepEqual(entries[0].ForgeReqs.Env.Required, []string{"API_KEY"}) {
		t.Errorf("Env.Required = %v, want [API_KEY]", entries[0].ForgeReqs.Env.Required)
	}
	if !reflect.DeepEqual(entries[0].ForgeReqs.Env.OneOf, []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"}) {
		t.Errorf("Env.OneOf = %v", entries[0].ForgeReqs.Env.OneOf)
	}
	if !reflect.DeepEqual(entries[0].ForgeReqs.Env.Optional, []string{"FIRECRAWL_API_KEY"}) {
		t.Errorf("Env.Optional = %v", entries[0].ForgeReqs.Env.Optional)
	}
}

func TestParseWithMetadata_UnknownNamespaces(t *testing.T) {
	input := `---
name: myskill
metadata:
  forge:
    requires:
      bins:
        - python
  clawdbot:
    priority: high
---
## Tool: myskill
Does things.
`
	entries, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata")
	}
	// clawdbot namespace should be tolerated
	if _, ok := meta.Metadata["clawdbot"]; !ok {
		t.Error("expected clawdbot namespace in metadata")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ForgeReqs == nil {
		t.Fatal("expected non-nil ForgeReqs")
	}
	if !reflect.DeepEqual(entries[0].ForgeReqs.Bins, []string{"python"}) {
		t.Errorf("Bins = %v, want [python]", entries[0].ForgeReqs.Bins)
	}
}

func TestParseWithMetadata_EmptyFrontmatter(t *testing.T) {
	input := `---
---
## Tool: simple
A simple tool.
`
	entries, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata (even if empty)")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ForgeReqs != nil {
		t.Error("expected nil ForgeReqs for empty frontmatter")
	}
}

func TestParseWithMetadata_FrontmatterOverridesName(t *testing.T) {
	input := `---
name: frontmatter-name
description: From frontmatter
---
## Tool: tool-name
Tool description.
`
	entries, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta.Name != "frontmatter-name" {
		t.Errorf("meta.Name = %q, want frontmatter-name", meta.Name)
	}
	if meta.Description != "From frontmatter" {
		t.Errorf("meta.Description = %q, want 'From frontmatter'", meta.Description)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "tool-name" {
		t.Errorf("entry name = %q, want tool-name", entries[0].Name)
	}
	if entries[0].Metadata != meta {
		t.Error("expected entry metadata to point to same SkillMetadata")
	}
}

func TestParseWithMetadata_EgressDomains(t *testing.T) {
	input := `---
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
      - github.com
---
## Tool: github_create_issue
Create a GitHub issue.
`
	entries, _, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ForgeReqs == nil {
		t.Fatal("expected non-nil ForgeReqs")
	}
	if !reflect.DeepEqual(entries[0].ForgeReqs.Bins, []string{"gh"}) {
		t.Errorf("Bins = %v, want [gh]", entries[0].ForgeReqs.Bins)
	}
}

func TestParseWithMetadata_ValidCategoryAndTags(t *testing.T) {
	input := `---
name: k8s-triage
description: Triage Kubernetes issues
category: sre
tags:
  - kubernetes
  - incident-response
  - triage
---
## Tool: k8s_triage
Diagnose Kubernetes workloads.
`
	entries, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta.Category != "sre" {
		t.Errorf("Category = %q, want sre", meta.Category)
	}
	if !reflect.DeepEqual(meta.Tags, []string{"kubernetes", "incident-response", "triage"}) {
		t.Errorf("Tags = %v", meta.Tags)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestParseWithMetadata_MissingCategoryAndTags(t *testing.T) {
	input := `---
name: simple
description: Simple skill
---
## Tool: simple_tool
Does things.
`
	_, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta.Category != "" {
		t.Errorf("Category should be empty, got %q", meta.Category)
	}
	if meta.Tags != nil {
		t.Errorf("Tags should be nil, got %v", meta.Tags)
	}
}

func TestParseWithMetadata_UppercaseCategoryNormalized(t *testing.T) {
	input := `---
name: myskill
category: SRE
---
## Tool: my_tool
Does things.
`
	_, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if meta.Category != "sre" {
		t.Errorf("Category = %q, want sre (normalized)", meta.Category)
	}
}

func TestParseWithMetadata_CategoryWithSpacesRejected(t *testing.T) {
	input := `---
name: myskill
category: "dev tools"
---
## Tool: my_tool
Does things.
`
	_, _, err := ParseWithMetadata(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for category with spaces")
	}
}

func TestParseWithMetadata_DuplicateTagsDeduplicated(t *testing.T) {
	input := `---
name: myskill
tags:
  - kubernetes
  - triage
  - kubernetes
  - triage
  - observability
---
## Tool: my_tool
Does things.
`
	_, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	want := []string{"kubernetes", "triage", "observability"}
	if !reflect.DeepEqual(meta.Tags, want) {
		t.Errorf("Tags = %v, want %v (deduplicated)", meta.Tags, want)
	}
}

func TestParseWithMetadata_TagWithUnderscoresRejected(t *testing.T) {
	input := `---
name: myskill
tags:
  - valid-tag
  - invalid_tag
---
## Tool: my_tool
Does things.
`
	_, _, err := ParseWithMetadata(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for tag with underscores")
	}
}

func TestParseWithMetadata_BodyCaptured(t *testing.T) {
	input := `---
name: k8s-triage
description: Kubernetes incident triage
metadata:
  forge:
    requires:
      bins:
        - kubectl
---
## Tool: k8s_triage

Diagnose unhealthy Kubernetes workloads.

**Input:** namespace and resource
**Output:** triage report

## Detection Heuristics

- Check pod status and restart counts
- Inspect OOMKilled containers

## Safety Constraints

- Never delete resources
`
	entries, _, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Body == "" {
		t.Fatal("expected non-empty Body")
	}
	if !strings.Contains(entries[0].Body, "## Detection Heuristics") {
		t.Error("Body should contain '## Detection Heuristics'")
	}
	if !strings.Contains(entries[0].Body, "## Safety Constraints") {
		t.Error("Body should contain '## Safety Constraints'")
	}
	if !strings.Contains(entries[0].Body, "## Tool: k8s_triage") {
		t.Error("Body should contain '## Tool: k8s_triage'")
	}
}

func TestParseWithMetadata_BodyCapturedNoFrontmatter(t *testing.T) {
	input := `## Tool: simple
A simple tool.

**Input:** text
`
	entries, _, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Body == "" {
		t.Fatal("expected non-empty Body even without frontmatter")
	}
	if !strings.Contains(entries[0].Body, "## Tool: simple") {
		t.Error("Body should contain tool heading")
	}
}

func TestParseWithMetadata_EmptyTagsArray(t *testing.T) {
	input := `---
name: myskill
tags: []
---
## Tool: my_tool
Does things.
`
	_, meta, err := ParseWithMetadata(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWithMetadata error: %v", err)
	}
	if len(meta.Tags) != 0 {
		t.Errorf("Tags should be empty, got %v", meta.Tags)
	}
}
