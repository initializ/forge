package local

import (
	"strings"
	"testing"
)

func TestEmbeddedRegistry_DiscoverAll(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	if len(skills) != 16 {
		names := make([]string, len(skills))
		for i, s := range skills {
			names[i] = s.Name
		}
		t.Fatalf("expected 16 skills, got %d: %v", len(skills), names)
	}

	// Verify all expected skills are present
	expectedSkills := map[string]struct {
		displayName   string
		hasEnv        bool
		hasBins       bool
		hasEgress     bool
		hasCapability bool
	}{
		"code-agent":            {displayName: "Code Agent", hasEnv: false, hasBins: false, hasEgress: false},
		"code-plan":             {displayName: "Code Plan", hasEnv: false, hasBins: true, hasEgress: true},
		"github":                {displayName: "Github", hasEnv: false, hasBins: true, hasEgress: true},
		"weather":               {displayName: "Weather", hasEnv: false, hasBins: true, hasEgress: true},
		"tavily-search":         {displayName: "Tavily Search", hasEnv: true, hasBins: true, hasEgress: true},
		"tavily-research":       {displayName: "Tavily Research", hasEnv: true, hasBins: true, hasEgress: true},
		"k8s-incident-triage":   {displayName: "K8s Incident Triage", hasEnv: false, hasBins: true, hasEgress: false},
		"code-review":           {displayName: "Code Review", hasEnv: false, hasBins: true, hasEgress: true},
		"code-review-standards": {displayName: "Code Review Standards", hasEnv: false, hasBins: false, hasEgress: false},
		"code-review-github":    {displayName: "Code Review Github", hasEnv: true, hasBins: true, hasEgress: true},
		"codegen-react":         {displayName: "Codegen React", hasEnv: false, hasBins: true, hasEgress: true},
		"codegen-html":          {displayName: "Codegen Html", hasEnv: false, hasBins: true, hasEgress: true},
		"k8s-pod-rightsizer":    {displayName: "K8s Pod Rightsizer", hasEnv: false, hasBins: true, hasEgress: false},
		"k8s-cost-visibility":   {displayName: "K8s Cost Visibility", hasEnv: false, hasBins: true, hasEgress: true},
		"linear":                {displayName: "Linear", hasEnv: true, hasBins: true, hasEgress: true},
		// web-browse ships with an empty egress_domains (operator must add
		// hosts before use), so hasEgress is false and hasCapability is true.
		"web-browse": {displayName: "Web Browse", hasEnv: false, hasBins: false, hasEgress: false, hasCapability: true},
	}

	for _, s := range skills {
		exp, ok := expectedSkills[s.Name]
		if !ok {
			t.Errorf("unexpected skill %q", s.Name)
			continue
		}
		if s.DisplayName != exp.displayName {
			t.Errorf("skill %q: DisplayName = %q, want %q", s.Name, s.DisplayName, exp.displayName)
		}
		if s.Description == "" {
			t.Errorf("skill %q: empty Description", s.Name)
		}
		if exp.hasEnv && len(s.RequiredEnv) == 0 {
			t.Errorf("skill %q: expected RequiredEnv", s.Name)
		}
		if exp.hasBins && len(s.RequiredBins) == 0 {
			t.Errorf("skill %q: expected RequiredBins", s.Name)
		}
		if exp.hasEgress && len(s.EgressDomains) == 0 {
			t.Errorf("skill %q: expected EgressDomains", s.Name)
		}
		if exp.hasCapability && len(s.Capabilities) == 0 {
			t.Errorf("skill %q: expected Capabilities", s.Name)
		}
		if !exp.hasCapability && len(s.Capabilities) > 0 {
			t.Errorf("skill %q: unexpected Capabilities %v", s.Name, s.Capabilities)
		}
	}
}

func TestEmbeddedRegistry_GitHubDetails(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	s := reg.Get("github")
	if s == nil {
		t.Fatal("Get(\"github\") returned nil")
	}
	if s.Description != "Create issues, PRs, clone repos, and manage git workflows" {
		t.Errorf("Description = %q", s.Description)
	}
	if s.Icon != "🐙" {
		t.Errorf("Icon = %q, want 🐙", s.Icon)
	}
	if len(s.RequiredEnv) != 0 {
		t.Errorf("RequiredEnv = %v, want empty (GH_TOKEN is optional)", s.RequiredEnv)
	}
	expectedBins := []string{"gh", "git", "jq"}
	if len(s.RequiredBins) != len(expectedBins) {
		t.Errorf("RequiredBins = %v, want %v", s.RequiredBins, expectedBins)
	}

	foundDomain := false
	for _, d := range s.EgressDomains {
		if d == "api.github.com" {
			foundDomain = true
		}
	}
	if !foundDomain {
		t.Errorf("EgressDomains = %v, want api.github.com", s.EgressDomains)
	}
}

func TestEmbeddedRegistry_TavilySearchDetails(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	s := reg.Get("tavily-search")
	if s == nil {
		t.Fatal("Get(\"tavily-search\") returned nil")
	}
	if len(s.RequiredEnv) != 1 || s.RequiredEnv[0] != "TAVILY_API_KEY" {
		t.Errorf("RequiredEnv = %v", s.RequiredEnv)
	}
	if len(s.RequiredBins) < 2 {
		t.Errorf("RequiredBins = %v, want at least [curl, jq]", s.RequiredBins)
	}

	foundDomain := false
	for _, d := range s.EgressDomains {
		if d == "api.tavily.com" {
			foundDomain = true
		}
	}
	if !foundDomain {
		t.Errorf("EgressDomains = %v, want api.tavily.com", s.EgressDomains)
	}

	// Check script
	if !reg.HasScript("tavily-search") {
		t.Error("tavily-search should have a script")
	}
	script, err := reg.LoadScript("tavily-search")
	if err != nil {
		t.Fatalf("LoadScript error: %v", err)
	}
	if !strings.Contains(string(script), "TAVILY_API_KEY") {
		t.Error("script should reference TAVILY_API_KEY")
	}
}

func TestEmbeddedRegistry_TavilyResearchDetails(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	s := reg.Get("tavily-research")
	if s == nil {
		t.Fatal("Get(\"tavily-research\") returned nil")
	}
	if s.Description != "Deep multi-source research using Tavily Research API" {
		t.Errorf("Description = %q", s.Description)
	}
	if len(s.RequiredEnv) != 1 || s.RequiredEnv[0] != "TAVILY_API_KEY" {
		t.Errorf("RequiredEnv = %v", s.RequiredEnv)
	}
	if len(s.RequiredBins) < 2 {
		t.Errorf("RequiredBins = %v, want at least [curl, jq]", s.RequiredBins)
	}

	foundDomain := false
	for _, d := range s.EgressDomains {
		if d == "api.tavily.com" {
			foundDomain = true
		}
	}
	if !foundDomain {
		t.Errorf("EgressDomains = %v, want api.tavily.com", s.EgressDomains)
	}

	// Check script
	if !reg.HasScript("tavily-research") {
		t.Error("tavily-research should have a script")
	}
	script, err := reg.LoadScript("tavily-research")
	if err != nil {
		t.Fatalf("LoadScript error: %v", err)
	}
	if !strings.Contains(string(script), "TAVILY_API_KEY") {
		t.Error("script should reference TAVILY_API_KEY")
	}
	if !strings.Contains(string(script), "api.tavily.com/research") {
		t.Error("script should call the research endpoint")
	}

	// Check timeout hint
	if s.TimeoutHint != 300 {
		t.Errorf("TimeoutHint = %d, want 300", s.TimeoutHint)
	}
}

func TestEmbeddedRegistry_LinearDetails(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	s := reg.Get("linear")
	if s == nil {
		t.Fatal("Get(\"linear\") returned nil")
	}
	if s.Icon != "📋" {
		t.Errorf("Icon = %q, want 📋", s.Icon)
	}
	if s.Category != "project-management" {
		t.Errorf("Category = %q, want project-management", s.Category)
	}
	if len(s.RequiredEnv) != 1 || s.RequiredEnv[0] != "LINEAR_API_KEY" {
		t.Errorf("RequiredEnv = %v, want [LINEAR_API_KEY]", s.RequiredEnv)
	}
	if len(s.RequiredBins) < 2 {
		t.Errorf("RequiredBins = %v, want at least [curl, jq]", s.RequiredBins)
	}

	foundDomain := false
	for _, d := range s.EgressDomains {
		if d == "api.linear.app" {
			foundDomain = true
		}
	}
	if !foundDomain {
		t.Errorf("EgressDomains = %v, want api.linear.app", s.EgressDomains)
	}

	// Linear is a multi-script skill — verify all 6 per-tool scripts + the
	// sourced helper. (The helper is named common.sh, not _common.sh, because
	// //go:embed excludes files starting with '_'.)
	expectedScripts := []string{
		"common.sh",
		"linear-add-comment.sh",
		"linear-get-issue.sh",
		"linear-get-workflow-states.sh",
		"linear-list-my-issues.sh",
		"linear-search-issues.sh",
		"linear-update-issue-state.sh",
	}
	gotScripts := reg.ListScripts("linear")
	gotSet := make(map[string]bool, len(gotScripts))
	for _, n := range gotScripts {
		gotSet[n] = true
	}
	for _, want := range expectedScripts {
		if !gotSet[want] {
			t.Errorf("missing expected script %q (got: %v)", want, gotScripts)
		}
	}

	// Verify the SKILL.md content references the canonical tool names.
	content, err := reg.LoadContent("linear")
	if err != nil {
		t.Fatalf("LoadContent error: %v", err)
	}
	for _, tool := range []string{
		"linear_get_issue",
		"linear_search_issues",
		"linear_list_my_issues",
		"linear_get_workflow_states",
		"linear_update_issue_state",
		"linear_add_comment",
	} {
		if !strings.Contains(string(content), "## Tool: "+tool) {
			t.Errorf("SKILL.md missing '## Tool: %s' heading", tool)
		}
	}
}

func TestEmbeddedRegistry_CodePlanDetails(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	s := reg.Get("code-plan")
	if s == nil {
		t.Fatal("Get(\"code-plan\") returned nil")
	}
	if s.Icon != "🗺️" {
		t.Errorf("Icon = %q, want 🗺️", s.Icon)
	}
	if s.Category != "developer" {
		t.Errorf("Category = %q, want developer", s.Category)
	}
	if len(s.OneOfEnv) != 2 {
		t.Errorf("OneOfEnv = %v, want [ANTHROPIC_API_KEY OPENAI_API_KEY]", s.OneOfEnv)
	}
	if len(s.RequiredBins) < 3 {
		t.Errorf("RequiredBins = %v, want at least [curl, jq, git]", s.RequiredBins)
	}
	if s.TimeoutHint != 180 {
		t.Errorf("TimeoutHint = %d, want 180", s.TimeoutHint)
	}

	foundAnthropic := false
	foundOpenAI := false
	for _, d := range s.EgressDomains {
		switch d {
		case "api.anthropic.com":
			foundAnthropic = true
		case "api.openai.com":
			foundOpenAI = true
		}
	}
	if !foundAnthropic || !foundOpenAI {
		t.Errorf("EgressDomains = %v, want api.anthropic.com AND api.openai.com", s.EgressDomains)
	}

	// Multi-script skill — verify both per-tool scripts + the sourced helper.
	expectedScripts := []string{
		"common.sh",
		"code-plan-create.sh",
		"code-plan-validate.sh",
	}
	gotScripts := reg.ListScripts("code-plan")
	gotSet := make(map[string]bool, len(gotScripts))
	for _, n := range gotScripts {
		gotSet[n] = true
	}
	for _, want := range expectedScripts {
		if !gotSet[want] {
			t.Errorf("missing expected script %q (got: %v)", want, gotScripts)
		}
	}

	// Verify the SKILL.md content references the canonical tool names.
	content, err := reg.LoadContent("code-plan")
	if err != nil {
		t.Fatalf("LoadContent error: %v", err)
	}
	for _, tool := range []string{"code_plan_create", "code_plan_validate"} {
		if !strings.Contains(string(content), "## Tool: "+tool) {
			t.Errorf("SKILL.md missing '## Tool: %s' heading", tool)
		}
	}
}

func TestEmbeddedRegistry_AllSkillsHaveCategoryAndTags(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	for _, s := range skills {
		if s.Category == "" {
			t.Errorf("skill %q has no category — add 'category:' to its SKILL.md frontmatter", s.Name)
		}
		if len(s.Tags) == 0 {
			t.Errorf("skill %q has no tags — add 'tags:' to its SKILL.md frontmatter", s.Name)
		}
	}
}

func TestEmbeddedRegistry_AllSkillsHaveIcons(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	skills, err := reg.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}

	for _, s := range skills {
		if s.Icon == "" {
			t.Errorf("skill %q has no icon — add 'icon:' to its SKILL.md frontmatter", s.Name)
		}
	}
}

func TestEmbeddedRegistry_LoadContent(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	skills, _ := reg.List()
	for _, s := range skills {
		content, err := reg.LoadContent(s.Name)
		if err != nil {
			t.Errorf("LoadContent(%q) error: %v", s.Name, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("LoadContent(%q) returned empty content", s.Name)
		}
		// Capability skills (e.g. requires.capabilities: [browser]) are
		// instructional: they teach the LLM to drive runtime-registered tools
		// and declare no '## Tool:' script entries of their own. Decide from
		// the parsed descriptor, not a substring of the raw content.
		if len(s.Capabilities) > 0 {
			continue
		}
		if !strings.Contains(string(content), "## Tool:") {
			t.Errorf("LoadContent(%q) missing '## Tool:' heading", s.Name)
		}
	}
}

func TestEmbeddedRegistry_NonexistentSkill(t *testing.T) {
	reg, err := NewEmbeddedRegistry()
	if err != nil {
		t.Fatalf("NewEmbeddedRegistry error: %v", err)
	}

	if reg.Get("nonexistent") != nil {
		t.Error("Get(\"nonexistent\") should return nil")
	}
	if reg.HasScript("nonexistent") {
		t.Error("HasScript(\"nonexistent\") should return false")
	}
}
