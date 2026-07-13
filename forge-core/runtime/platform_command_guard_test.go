package runtime

import "testing"

// TestPlatformCommandGuard_MatchTargets pins the shared match-target
// semantics (#238): cli_execute matches on the reconstructed command line,
// every other tool matches on the raw tool-input JSON — identical to skill
// deny_commands so operators and skill authors author patterns the same way.
func TestPlatformCommandGuard_MatchTargets(t *testing.T) {
	g, err := NewPlatformCommandGuard([]PlatformCommandSpec{
		{Pattern: `kubectl\s+delete`, Message: "no destructive kubectl", LayerSource: "system", LayerPath: "/etc/forge/policy.yaml"},
	})
	if err != nil {
		t.Fatalf("build guard: %v", err)
	}

	t.Run("cli_execute command line", func(t *testing.T) {
		m := g.Match("cli_execute", `{"binary":"kubectl","args":["delete","pod","foo"]}`)
		if m == nil {
			t.Fatal("expected a match on the reconstructed command line")
		}
		if m.LayerSource != "system" || m.Message != "no destructive kubectl" {
			t.Errorf("attribution wrong: %+v", m)
		}
	})

	t.Run("non-cli_execute raw JSON", func(t *testing.T) {
		if m := g.Match("mcp__k8s__run", `{"cmd":"kubectl delete pod foo"}`); m == nil {
			t.Error("expected a match against the raw tool-input JSON of a non-cli_execute tool")
		}
	})

	t.Run("no false positive", func(t *testing.T) {
		if m := g.Match("cli_execute", `{"binary":"kubectl","args":["get","pods"]}`); m != nil {
			t.Errorf("kubectl get should not match kubectl delete; got %+v", m)
		}
	})

	// #238 review: a JSON-escaped separator (\t here) must NOT let a call
	// slip past a whitespace-sensitive pattern for a non-cli_execute tool.
	// The downstream tool decodes "kubectl\tdelete" to a real TAB, so the
	// guard must match against the decoded value, not just the raw JSON.
	t.Run("json-escape evasion is caught", func(t *testing.T) {
		// The Go string below contains the two-char JSON escape \t (backslash-t),
		// i.e. what an attacker would put on the wire.
		if m := g.Match("mcp__k8s__run", `{"cmd":"kubectl\tdelete pod foo"}`); m == nil {
			t.Error("JSON-escaped whitespace evaded the pattern; decoded values must be matched")
		}
	})
}

// TestPlatformCommandGuard_FailsClosedOnInvalidRegex is the AC guard: an
// uncompilable pattern returns an error (the runner turns this into a
// startup abort) rather than silently dropping the rule.
func TestPlatformCommandGuard_FailsClosedOnInvalidRegex(t *testing.T) {
	_, err := NewPlatformCommandGuard([]PlatformCommandSpec{
		{Pattern: `valid`, LayerSource: "system"},
		{Pattern: `(unclosed`, LayerSource: "workspace", LayerPath: "/ws/policy.yaml"},
	})
	if err == nil {
		t.Fatal("expected an error for the invalid regex")
	}
}

// TestPlatformCommandGuard_Empty confirms the nil-safe no-op path so the
// runner can skip hook registration when no layer declares patterns.
func TestPlatformCommandGuard_Empty(t *testing.T) {
	var nilGuard *PlatformCommandGuard
	if !nilGuard.Empty() {
		t.Error("nil guard should report Empty")
	}
	g, _ := NewPlatformCommandGuard(nil)
	if !g.Empty() {
		t.Error("guard built from no specs should report Empty")
	}
	if m := nilGuard.Match("cli_execute", `{"binary":"rm"}`); m != nil {
		t.Error("nil guard Match should return nil, not panic")
	}
}
