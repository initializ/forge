package optimizer

import (
	"path/filepath"
	"testing"
)

func TestWorkingDirFromBody(t *testing.T) {
	// System as a block array carrying Claude Code's env block.
	body := []byte(`{"model":"m","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"<env>\nWorking directory: /Users/me/proj/denyabot-python\nIs a git repo: Yes\n</env>"}],"messages":[]}`)
	if got := workingDirFromBody(body); got != "/Users/me/proj/denyabot-python" {
		t.Fatalf("workingDirFromBody = %q", got)
	}
	// Absent → empty.
	if got := workingDirFromBody([]byte(`{"model":"m","system":"no cwd here","messages":[]}`)); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestResolveRepo_PerSessionScope(t *testing.T) {
	store, _ := NewFileMemoryStore(filepath.Join(t.TempDir(), "m.jsonl"))
	// Resolver echoes the basename (stands in for git toplevel).
	former := NewMemoryFormer(MemoryFormerConfig{
		Store: store, Repo: "launch-dir", Commit: "aaa",
		RepoResolver: func(cwd string) (string, string) { return filepath.Base(cwd), "sha1" },
	})

	bodyA := []byte(`{"model":"m","system":[{"type":"text","text":"Working directory: /Users/me/repoA"}],"messages":[]}`)
	bodyB := []byte(`{"model":"m","system":[{"type":"text","text":"Working directory: /Users/me/repoB"}],"messages":[]}`)
	noCwd := []byte(`{"model":"m","system":"nothing","messages":[]}`)

	if r, c := former.resolveRepo(bodyA); r != "repoA" || c != "sha1" {
		t.Errorf("repoA scope = (%q,%q)", r, c)
	}
	if r, _ := former.resolveRepo(bodyB); r != "repoB" {
		t.Errorf("repoB scope = %q — sessions in different repos must not collide", r)
	}
	// No working dir → falls back to the launch repo.
	if r, c := former.resolveRepo(noCwd); r != "launch-dir" || c != "aaa" {
		t.Errorf("fallback scope = (%q,%q)", r, c)
	}
}
