package optimizer

import (
	"path/filepath"
	"strings"
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
	former := newFormer(t, MemoryFormerConfig{
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

// TestResolveRepo_BasenameCollisionDisambiguated guards MEDIUM #5: with no git
// resolver the scope falls back to the working-dir basename, so two distinct
// checkouts that share a basename (~/a/client vs ~/work/client) must NOT collapse
// to one scope and cross-inject. The fix appends a short path hash.
func TestResolveRepo_BasenameCollisionDisambiguated(t *testing.T) {
	store, _ := NewFileMemoryStore(filepath.Join(t.TempDir(), "m.jsonl"))
	former := newFormer(t, MemoryFormerConfig{Store: store}) // no RepoResolver → basename+hash

	bodyA := []byte(`{"model":"m","system":[{"type":"text","text":"Working directory: /home/u/a/client"}],"messages":[]}`)
	bodyB := []byte(`{"model":"m","system":[{"type":"text","text":"Working directory: /home/u/work/client"}],"messages":[]}`)

	rA, _ := former.resolveRepo(bodyA)
	rB, _ := former.resolveRepo(bodyB)
	if rA == rB {
		t.Fatalf("distinct paths collided to the same scope %q — memory would cross-inject", rA)
	}
	if !strings.HasPrefix(rA, "client-") || !strings.HasPrefix(rB, "client-") {
		t.Errorf("expected basename-prefixed scopes, got %q and %q", rA, rB)
	}
	// Same path must be stable (memoized) across calls.
	if rA2, _ := former.resolveRepo(bodyA); rA2 != rA {
		t.Errorf("scope not stable for the same path: %q then %q", rA, rA2)
	}
}
