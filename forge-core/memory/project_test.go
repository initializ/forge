package memory

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	const want = "github.com/initializ/forge"
	cases := []string{
		"git@github.com:initializ/forge.git",
		"https://github.com/initializ/forge.git",
		"https://github.com/initializ/forge",
		"ssh://git@github.com/initializ/forge.git",
		"ssh://git@github.com:22/initializ/forge",
		"https://user:token@github.com/initializ/forge/",
		"GIT@GitHub.com:initializ/Forge.git",
		"  git@github.com:initializ/forge.git  ",
	}
	for _, in := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectIDLocalFallback(t *testing.T) {
	dir := t.TempDir()
	id := ProjectID(dir)
	if !strings.HasPrefix(id, "local-") {
		t.Fatalf("expected local- prefix for non-git dir, got %q", id)
	}
	if got := ProjectID(dir); got != id {
		t.Errorf("ProjectID not deterministic: %q vs %q", id, got)
	}
	if other := ProjectID(t.TempDir()); other == id {
		t.Errorf("distinct dirs collapsed to same id %q", id)
	}
}

func TestProjectID(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("remote", "add", "origin", "git@github.com:initializ/forge.git")
	idA := ProjectID(dir)
	if len(idA) != 12 {
		t.Errorf("expected 12-char id, got %q", idA)
	}
	// An equivalent remote URL must yield the same id.
	git("remote", "set-url", "origin", "https://github.com/initializ/forge.git")
	if idB := ProjectID(dir); idB != idA {
		t.Errorf("equivalent remotes gave different ids: %q vs %q", idA, idB)
	}
}

func TestAgentID(t *testing.T) {
	cases := map[string]string{
		"weather-agent": "weather-agent",
		"Weather Agent": "weather-agent",
		"  My_Agent!! ": "my_agent",
		"UPPER":         "upper",
		"":              "unknown-agent",
		"///":           "unknown-agent",
	}
	for in, want := range cases {
		if got := AgentID(in); got != want {
			t.Errorf("AgentID(%q) = %q, want %q", in, got, want)
		}
	}
}
