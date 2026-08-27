package runtime

import (
	"reflect"
	"slices"
	"testing"
)

func TestSkillScriptNameVariants(t *testing.T) {
	cases := []struct {
		tool string
		want []string
	}{
		{"some_name", []string{"some-name", "some_name"}}, // both forms, hyphen first
		{"some-name", []string{"some-name", "some_name"}}, // symmetric
		{"plain", []string{"plain"}},                      // no separator → single form
		{"a_b-c", []string{"a-b-c", "a_b_c"}},             // mixed separators normalize both ways
	}
	for _, tc := range cases {
		if got := skillScriptNameVariants(tc.tool); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("skillScriptNameVariants(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

func TestSkillScriptCandidatePaths_bothForms(t *testing.T) {
	got := SkillScriptCandidatePaths("myskill", "some_name")
	// A `## Tool: some_name` must be bindable by both scripts/some_name.sh and
	// scripts/some-name.sh (#418), skill-local before shared, shell before py.
	want := []string{
		"skills/myskill/scripts/some-name.sh",
		"skills/myskill/scripts/some_name.sh",
		"skills/myskill/scripts/some-name.bash",
		"skills/myskill/scripts/some_name.bash",
		"skills/myskill/scripts/some-name.py",
		"skills/myskill/scripts/some_name.py",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("candidate %q missing from %v", w, got)
		}
	}
	// Ordering: skill-local sh (either name) precedes any shared-dir candidate,
	// and precedes skill-local python.
	if idxOf(got, "skills/myskill/scripts/some_name.sh") > idxOf(got, "skills/myskill/scripts/some-name.py") {
		t.Error("shell should rank before python within a directory")
	}
	if idxOf(got, "skills/myskill/scripts/some-name.sh") > idxOf(got, "skills/scripts/some-name.sh") {
		t.Error("skill-local dir should rank before shared dir")
	}
	// Hyphen before underscore within the same ext.
	if idxOf(got, "skills/myskill/scripts/some-name.sh") > idxOf(got, "skills/myskill/scripts/some_name.sh") {
		t.Error("hyphenated name should rank before underscore for back-compat")
	}
}

func TestSkillScriptCandidatePaths_traversalRejected(t *testing.T) {
	for _, bad := range []string{"../../etc/passwd", "a/b", "..", "foo/../bar"} {
		if got := SkillScriptCandidatePaths("s", bad); got != nil {
			t.Errorf("SkillScriptCandidatePaths(%q) = %v, want nil (traversal must be rejected)", bad, got)
		}
	}
}

func idxOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}
