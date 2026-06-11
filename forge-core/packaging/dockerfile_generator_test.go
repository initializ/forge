package packaging

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

func TestGenerateDockerfile_NoBins(t *testing.T) {
	frags, warnings, err := GenerateDockerfile(nil, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if frags.PreAppStages != "" {
		t.Errorf("expected empty PreAppStages, got %q", frags.PreAppStages)
	}
	if len(frags.BinCopies) != 0 {
		t.Errorf("expected no bin copies, got %v", frags.BinCopies)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

// TestGenerateDockerfile_AptBatch_RunsInAppStage pins the fix for
// issue #149: apt-installed binaries must NOT be routed through the
// bins stage (where they'd land in /usr/bin/ and never reach the
// application image), they must be returned as RuntimeAptPackages
// for the app stage to install directly. Per-binary lib/etc deps
// then come along via apt's dependency resolution.
func TestGenerateDockerfile_AptBatch_RunsInAppStage(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
			{Name: "curl"},
		},
	}

	frags, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PreAppStages should be empty — no bins stage is needed for
	// apt-only requirements (the agent's apt deps are installed in the
	// application stage where they can reach the runtime).
	if frags.PreAppStages != "" {
		t.Errorf("apt-only requirements should not emit a bins stage; got PreAppStages:\n%s", frags.PreAppStages)
	}

	// Runtime apt packages must include both declared bins.
	wantPkgs := map[string]bool{"jq": false, "curl": false}
	for _, p := range frags.RuntimeAptPackages {
		if _, ok := wantPkgs[p]; ok {
			wantPkgs[p] = true
		}
	}
	for pkg, found := range wantPkgs {
		if !found {
			t.Errorf("RuntimeAptPackages missing %q; got %v", pkg, frags.RuntimeAptPackages)
		}
	}

	// No BinCopies for apt installs — apt does the placement at /usr/bin.
	for _, c := range frags.BinCopies {
		if strings.Contains(c, "jq") || strings.Contains(c, "curl") {
			t.Errorf("apt-installed bins should not appear in BinCopies; got %q", c)
		}
	}
}

func TestGenerateDockerfile_Alpine_ApkRunsInAppStage(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "jq"},
		},
	}

	frags, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if frags.PreAppStages != "" {
		t.Errorf("apk-only requirements should not emit a bins stage; got:\n%s", frags.PreAppStages)
	}
	if len(frags.RuntimeApkPackages) == 0 || frags.RuntimeApkPackages[0] != "jq" {
		t.Errorf("expected RuntimeApkPackages=[jq], got %v", frags.RuntimeApkPackages)
	}
}

// TestGenerateDockerfile_DirectURL_RoutesThroughBinsStage confirms
// direct-URL binaries (e.g. kubectl from GitHub releases) flow through
// the shared bins stage and get forwarded with an explicit per-binary
// COPY — no more wholesale-directory copies.
func TestGenerateDockerfile_DirectURL_RoutesThroughBinsStage(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "kubectl"},
		},
	}

	frags, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(frags.PreAppStages, "FROM debian:bookworm-slim AS bins") {
		t.Errorf("direct-URL bin should produce a bins stage; got PreAppStages:\n%s", frags.PreAppStages)
	}
	if !strings.Contains(frags.PreAppStages, "curl -fsSL") {
		t.Errorf("expected curl download in bins stage; got:\n%s", frags.PreAppStages)
	}
	// Build-time curl/ca-certificates install in bins stage (scoped to
	// the stage; never propagates to the app image).
	if !strings.Contains(frags.PreAppStages, "apt-get install -y --no-install-recommends curl ca-certificates") {
		t.Errorf("expected build-time curl+ca-certs install in bins stage; got:\n%s", frags.PreAppStages)
	}

	// App-stage gets an explicit per-binary COPY — not a wholesale
	// /usr/local/bin/ directory copy.
	foundCopy := false
	for _, c := range frags.BinCopies {
		if strings.Contains(c, "COPY --from=bins /usr/local/bin/kubectl /usr/local/bin/kubectl") {
			foundCopy = true
		}
		if strings.Contains(c, "/usr/local/bin/ ") {
			t.Errorf("BinCopies must not emit wholesale-directory COPYs (issue #149); got %q", c)
		}
	}
	if !foundCopy {
		t.Errorf("BinCopies missing explicit COPY for kubectl; got %v", frags.BinCopies)
	}
}

func TestGenerateDockerfile_CustomBaseImage(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "kubectl"},
		},
	}

	cfg := types.PackageConfig{BaseImage: "ubuntu:24.04"}
	frags, _, err := GenerateDockerfile(manifest, cfg, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(frags.PreAppStages, "FROM ubuntu:24.04") {
		t.Errorf("expected custom base image ubuntu:24.04 in bins stage; got:\n%s", frags.PreAppStages)
	}
}

// TestGenerateDockerfile_ImageCopy_SkipsBinsStage confirms image-copy
// binaries (the heavy ones like playwright) come directly from their
// companion `bin-<name>` stage to the app stage, with no bins-stage
// hop. Pre-fix the app stage's wholesale `/usr/local/bin/` copy from
// bins meant playwright had to be routed through bins first.
func TestGenerateDockerfile_ImageCopy_SkipsBinsStage(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "playwright"},
		},
	}

	frags, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(frags.PreAppStages, "FROM mcr.microsoft.com/playwright") {
		t.Errorf("expected playwright companion image stage in PreAppStages; got:\n%s", frags.PreAppStages)
	}
	// Image-copy bins should NOT trigger a shared bins stage.
	if strings.Contains(frags.PreAppStages, "AS bins\n") {
		t.Errorf("image-copy-only manifest should not emit a shared bins stage; got:\n%s", frags.PreAppStages)
	}

	foundCopy := false
	for _, c := range frags.BinCopies {
		if strings.Contains(c, "COPY --from=bin-playwright") {
			foundCopy = true
		}
	}
	if !foundCopy {
		t.Errorf("BinCopies missing explicit COPY from playwright companion stage; got %v", frags.BinCopies)
	}
}

func TestGenerateDockerfile_LocalFile_EmittedInAppStageOnly(t *testing.T) {
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

	frags, _, err := GenerateDockerfile(manifest, cfg, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Local-file copies don't need a bins stage — they're COPY-from-context.
	if strings.Contains(frags.PreAppStages, "AS bins") {
		t.Errorf("local-file-only manifest should not emit a bins stage; got:\n%s", frags.PreAppStages)
	}

	foundCopy, foundChmod := false, false
	for _, c := range frags.BinCopies {
		if strings.Contains(c, "COPY .local-bins/forge /usr/local/bin/forge") {
			foundCopy = true
		}
		if strings.Contains(c, "RUN chmod 0755 /usr/local/bin/forge") {
			foundChmod = true
		}
	}
	if !foundCopy {
		t.Errorf("BinCopies missing local-file COPY; got %v", frags.BinCopies)
	}
	if !foundChmod {
		t.Errorf("BinCopies missing local-file chmod; got %v", frags.BinCopies)
	}
}

func TestGenerateDockerfile_AlpineBlockedByUbuntu(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "playwright"},
			{Name: "jq"},
		},
	}

	frags, warnings, err := GenerateDockerfile(manifest, types.PackageConfig{}, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PreAppStages is the only place a base image appears for this
	// manifest (jq → runtime apt, playwright → companion image stage).
	// The companion stage uses its own upstream image; the runtime
	// base is picked by the caller from the application stage's FROM
	// directive (not visible here).
	if !strings.Contains(frags.PreAppStages, "FROM mcr.microsoft.com/playwright") {
		t.Errorf("expected playwright companion image stage; got:\n%s", frags.PreAppStages)
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

// TestGenerateDockerfile_MixedAptAndDirectURL is the bundled
// code-review skill's shape: curl/git/jq via apt + gh via direct URL.
// Pre-fix the apt installs went through the bins stage and were lost
// when the app stage only copied /usr/local/bin/. Post-fix the apt
// installs run in the app stage AND there's a per-binary COPY for gh.
func TestGenerateDockerfile_MixedAptAndDirectURL(t *testing.T) {
	manifest := &BinManifest{
		Requirements: []contract.BinRequirement{
			{Name: "curl"},
			{Name: "git"},
			{Name: "jq"},
			{Name: "gh"}, // registry-known → direct URL
		},
	}

	frags, _, err := GenerateDockerfile(manifest, types.PackageConfig{}, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All three apt bins must show up as runtime packages.
	wantApt := map[string]bool{"curl": false, "git": false, "jq": false}
	for _, p := range frags.RuntimeAptPackages {
		if _, ok := wantApt[p]; ok {
			wantApt[p] = true
		}
	}
	for pkg, found := range wantApt {
		if !found {
			t.Errorf("RuntimeAptPackages missing %q (issue #149 — these would have been silently dropped pre-fix); got %v",
				pkg, frags.RuntimeAptPackages)
		}
	}

	// gh must have an explicit per-binary COPY from the bins stage.
	foundGH := false
	for _, c := range frags.BinCopies {
		if strings.Contains(c, "COPY --from=bins /usr/local/bin/gh /usr/local/bin/gh") {
			foundGH = true
		}
	}
	if !foundGH {
		t.Errorf("BinCopies missing explicit COPY for gh; got %v", frags.BinCopies)
	}
}
