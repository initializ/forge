package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

func TestBrowserRegistrationDecision(t *testing.T) {
	derived := &contract.DerivedBrowserConfig{SourceSkills: []string{"web-browse"}}
	cases := []struct {
		name     string
		derived  *contract.DerivedBrowserConfig
		binPath  string
		resErr   error
		proxyURL string
		wantOK   bool
		wantIn   string // substring of reason when !ok
	}{
		{"no capability", nil, "/usr/bin/chromium", nil, "http://127.0.0.1:1", false, ""},
		{"all present", derived, "/usr/bin/chromium", nil, "http://127.0.0.1:1", true, ""},
		{"no binary", derived, "", errors.New("not found"), "http://127.0.0.1:1", false, "FORGE_BROWSER_BIN"},
		{"no proxy", derived, "/usr/bin/chromium", nil, "", false, "unproxied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := browserRegistrationDecision(tc.derived, tc.binPath, tc.resErr, tc.proxyURL)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (reason: %s)", ok, tc.wantOK, reason)
			}
			if tc.wantIn != "" && !strings.Contains(reason, tc.wantIn) {
				t.Errorf("reason %q missing %q", reason, tc.wantIn)
			}
		})
	}
}

// writeSkill writes a SKILL.md under <dir>/skills/<name>/SKILL.md.
func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newBrowserTestRunner(t *testing.T, dir string) *Runner {
	t.Helper()
	return &Runner{
		cfg: RunnerConfig{
			Config:  &types.ForgeConfig{},
			WorkDir: dir,
		},
		logger: coreruntime.NewJSONLogger(discardWriter{}, false),
	}
}

// TestValidateSkillRequirements_CapabilityOnlySkill is the regression test for
// the early-return bug: a skill with capabilities + egress_domains but zero
// bins/env used to be dropped entirely (no derived config stored at all).
func TestValidateSkillRequirements_CapabilityOnlySkill(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "web-browse", `---
name: web-browse
description: Browse the web
metadata:
  forge:
    requires:
      capabilities:
        - browser
    egress_domains:
      - "docs.example.com"
---
## Tool: web_browse
Browse pages using the browser_* tools.
`)

	r := newBrowserTestRunner(t, dir)
	if err := r.validateSkillRequirements(nil); err != nil {
		t.Fatalf("validateSkillRequirements: %v", err)
	}

	if r.derivedBrowserConfig == nil {
		t.Fatal("derivedBrowserConfig = nil, want browser capability derived")
	}
	if !reflect.DeepEqual(r.derivedBrowserConfig.SourceSkills, []string{"web-browse"}) {
		t.Errorf("SourceSkills = %v, want [web-browse]", r.derivedBrowserConfig.SourceSkills)
	}
	if r.derivedCLIConfig == nil {
		t.Fatal("derivedCLIConfig = nil for capability-only skill; egress_domains were dropped (regression)")
	}
	if !reflect.DeepEqual(r.derivedCLIConfig.EgressDomains, []string{"docs.example.com"}) {
		t.Errorf("EgressDomains = %v, want [docs.example.com]", r.derivedCLIConfig.EgressDomains)
	}
	if len(r.derivedCLIConfig.AllowedBinaries) != 0 {
		t.Errorf("AllowedBinaries = %v, want none", r.derivedCLIConfig.AllowedBinaries)
	}
}

// TestValidateSkillRequirements_InstructionalSkill covers the metadata-only
// synthesis path: a SKILL.md with no "## Tool:" entries must still contribute
// its capabilities and egress domains.
func TestValidateSkillRequirements_InstructionalSkill(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "web-browse", `---
name: web-browse
description: Browse the web
metadata:
  forge:
    requires:
      capabilities:
        - browser
    egress_domains:
      - "docs.example.com"
---
This skill teaches the agent to drive the browser tools. No script tools.
`)

	r := newBrowserTestRunner(t, dir)
	if err := r.validateSkillRequirements(nil); err != nil {
		t.Fatalf("validateSkillRequirements: %v", err)
	}
	if r.derivedBrowserConfig == nil {
		t.Fatal("derivedBrowserConfig = nil for instructional skill")
	}
	if !reflect.DeepEqual(r.derivedBrowserConfig.SourceSkills, []string{"web-browse"}) {
		t.Errorf("SourceSkills = %v, want [web-browse]", r.derivedBrowserConfig.SourceSkills)
	}
	if r.derivedCLIConfig == nil || !reflect.DeepEqual(r.derivedCLIConfig.EgressDomains, []string{"docs.example.com"}) {
		t.Errorf("derivedCLIConfig = %+v, want egress docs.example.com", r.derivedCLIConfig)
	}
}

// TestValidateSkillRequirements_NoBrowserSkill: agents without the capability
// derive nothing browser-related.
func TestValidateSkillRequirements_NoBrowserSkill(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "github", `---
name: github
description: GitHub helper
metadata:
  forge:
    requires:
      bins: [curl, jq]
---
## Tool: gh_list
List things.
`)

	r := newBrowserTestRunner(t, dir)
	if err := r.validateSkillRequirements(nil); err != nil {
		t.Fatalf("validateSkillRequirements: %v", err)
	}
	if r.derivedBrowserConfig != nil {
		t.Errorf("derivedBrowserConfig = %+v, want nil without browser capability", r.derivedBrowserConfig)
	}
	if r.derivedCLIConfig == nil || len(r.derivedCLIConfig.AllowedBinaries) != 2 {
		t.Errorf("derivedCLIConfig = %+v, want curl+jq", r.derivedCLIConfig)
	}
}
