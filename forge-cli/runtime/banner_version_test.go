package runtime

import "testing"

// forgeVersionString is what the startup banner shows on the "Forge:" line —
// the forge binary/runtime version, distinct from the agent's forge.yaml
// version. It must degrade cleanly for dev builds (#335).
func TestForgeVersionString(t *testing.T) {
	tests := []struct {
		name    string
		version string
		commit  string
		want    string
	}{
		{"release with commit", "v0.17.0", "51df9a4", "v0.17.0 (commit: 51df9a4)"},
		{"release, no commit baked", "v0.17.0", "none", "v0.17.0"},
		{"release, empty commit", "v0.17.0", "", "v0.17.0"},
		{"dev build (defaults)", "dev", "none", "dev"},
		{"empty version defaults to dev", "", "none", "dev"},
		{"empty version, real commit", "", "abc1234", "dev (commit: abc1234)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{cfg: RunnerConfig{RuntimeVersion: tc.version, RuntimeCommit: tc.commit}}
			if got := r.forgeVersionString(); got != tc.want {
				t.Errorf("forgeVersionString(%q, %q) = %q, want %q", tc.version, tc.commit, got, tc.want)
			}
		})
	}
}
