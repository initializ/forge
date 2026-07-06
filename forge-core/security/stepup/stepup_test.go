package stepup

import (
	"errors"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
)

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"disabled is always valid", Config{}, false},
		{"enabled without tools rejected", Config{Enabled: true}, true},
		{"enabled with tools", Config{
			Enabled: true,
			Tools:   map[string]string{"cli_execute": "acr:mfa"},
		}, false},
		{"tool acr not in hierarchy rejected", Config{
			Enabled:      true,
			Tools:        map[string]string{"cli_execute": "acr:mfa"},
			AcrHierarchy: []string{"acr:password", "acr:hardware"}, // missing acr:mfa
		}, true},
		{"tool acr in hierarchy ok", Config{
			Enabled:      true,
			Tools:        map[string]string{"cli_execute": "acr:mfa"},
			AcrHierarchy: []string{"acr:password", "acr:mfa", "acr:hardware"},
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if (err != nil) != c.wantErr {
				t.Errorf("err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// TestCheck_ToolWithoutRequirementAllows — tools not listed in
// the step-up config pass through with no check.
func TestCheck_ToolWithoutRequirementAllows(t *testing.T) {
	e, err := New(Config{
		Enabled: true,
		Tools:   map[string]string{"cli_execute": "acr:mfa"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.Check("web_search", &auth.Identity{}); err != nil {
		t.Errorf("tool without requirement should pass, got %v", err)
	}
}

// TestCheck_StrictEqualMatches — no hierarchy configured, presented
// acr must equal required exactly.
func TestCheck_StrictEqualMatches(t *testing.T) {
	e, _ := New(Config{
		Enabled: true,
		Tools:   map[string]string{"cli_execute": "acr:mfa"},
	})
	id := &auth.Identity{Claims: map[string]any{"acr": "acr:mfa"}}
	if err := e.Check("cli_execute", id); err != nil {
		t.Errorf("exact match should pass: %v", err)
	}
}

// TestCheck_StrictEqualRejectsMismatch — different acr values
// don't cross-satisfy in strict-equal mode.
func TestCheck_StrictEqualRejectsMismatch(t *testing.T) {
	e, _ := New(Config{
		Enabled: true,
		Tools:   map[string]string{"cli_execute": "acr:mfa"},
	})
	id := &auth.Identity{Claims: map[string]any{"acr": "acr:hardware"}}
	err := e.Check("cli_execute", id)
	if err == nil {
		t.Fatal("expected step-up error on mismatched acr in strict-equal mode")
	}
	re, ok := AsRequiredError(err)
	if !ok {
		t.Fatalf("expected *RequiredError, got %T", err)
	}
	if re.RequiredAcr != "acr:mfa" {
		t.Errorf("RequiredAcr: got %q want acr:mfa", re.RequiredAcr)
	}
	if re.PresentedAcr != "acr:hardware" {
		t.Errorf("PresentedAcr: got %q want acr:hardware", re.PresentedAcr)
	}
}

// TestCheck_MissingIdentityFailsClosed — nil identity always fails
// step-up. Auth must succeed before step-up evaluates.
func TestCheck_MissingIdentityFailsClosed(t *testing.T) {
	e, _ := New(Config{
		Enabled: true,
		Tools:   map[string]string{"cli_execute": "acr:mfa"},
	})
	err := e.Check("cli_execute", nil)
	if err == nil {
		t.Fatal("expected error on nil identity")
	}
	re, _ := AsRequiredError(err)
	if re == nil {
		t.Fatalf("expected *RequiredError")
	}
	if re.PresentedAcr != "" {
		t.Errorf("PresentedAcr on nil identity: got %q", re.PresentedAcr)
	}
}

// TestCheck_MissingAcrClaimFailsClosed — identity with no `acr`
// claim → step-up required.
func TestCheck_MissingAcrClaimFailsClosed(t *testing.T) {
	e, _ := New(Config{
		Enabled: true,
		Tools:   map[string]string{"cli_execute": "acr:mfa"},
	})
	// Identity carries other claims but no `acr`.
	id := &auth.Identity{Claims: map[string]any{"sub": "user-1"}}
	err := e.Check("cli_execute", id)
	if err == nil {
		t.Fatal("expected step-up error")
	}
	re, _ := AsRequiredError(err)
	if !strContains(re.Reason, "no acr claim presented") {
		t.Errorf("reason should mention missing acr: %q", re.Reason)
	}
}

// TestCheck_HierarchyStrongerAcrSatisfies — hardware > mfa in the
// hierarchy, so a caller with hardware satisfies an mfa requirement.
func TestCheck_HierarchyStrongerAcrSatisfies(t *testing.T) {
	e, _ := New(Config{
		Enabled:      true,
		Tools:        map[string]string{"cli_execute": "acr:mfa"},
		AcrHierarchy: []string{"acr:password", "acr:mfa", "acr:hardware"},
	})
	id := &auth.Identity{Claims: map[string]any{"acr": "acr:hardware"}}
	if err := e.Check("cli_execute", id); err != nil {
		t.Errorf("hardware > mfa should satisfy: %v", err)
	}
}

// TestCheck_HierarchyWeakerAcrFails — password < mfa, so a caller
// with password fails an mfa requirement.
func TestCheck_HierarchyWeakerAcrFails(t *testing.T) {
	e, _ := New(Config{
		Enabled:      true,
		Tools:        map[string]string{"cli_execute": "acr:mfa"},
		AcrHierarchy: []string{"acr:password", "acr:mfa", "acr:hardware"},
	})
	id := &auth.Identity{Claims: map[string]any{"acr": "acr:password"}}
	err := e.Check("cli_execute", id)
	if err == nil {
		t.Fatal("password < mfa should require step-up")
	}
}

// TestCheck_HierarchyUnknownAcrFails — an acr not listed in the
// hierarchy is treated as weaker than any listed level (fail-closed).
func TestCheck_HierarchyUnknownAcrFails(t *testing.T) {
	e, _ := New(Config{
		Enabled:      true,
		Tools:        map[string]string{"cli_execute": "acr:mfa"},
		AcrHierarchy: []string{"acr:password", "acr:mfa", "acr:hardware"},
	})
	id := &auth.Identity{Claims: map[string]any{"acr": "acr:something-weird"}}
	err := e.Check("cli_execute", id)
	if err == nil {
		t.Fatal("unknown acr should fail closed")
	}
}

// TestRequiredError_ImplementsError satisfies the errors.As
// contract used by the runner middleware.
func TestRequiredError_ImplementsError(t *testing.T) {
	original := &RequiredError{Tool: "cli_execute", RequiredAcr: "acr:mfa"}
	wrapped := &wrapErr{inner: original}
	re, ok := AsRequiredError(wrapped)
	if !ok {
		t.Fatal("errors.As should unwrap through wrapErr")
	}
	if re.Tool != "cli_execute" {
		t.Errorf("Tool: got %q", re.Tool)
	}
	if _, ok := AsRequiredError(errors.New("plain")); ok {
		t.Error("AsRequiredError should return false for plain errors")
	}
}

func TestRequirementFor(t *testing.T) {
	e, _ := New(Config{
		Enabled: true,
		Tools:   map[string]string{"cli_execute": "acr:mfa"},
	})
	if got := e.RequirementFor("cli_execute"); got != "acr:mfa" {
		t.Errorf("got %q", got)
	}
	if got := e.RequirementFor("web_search"); got != "" {
		t.Errorf("expected empty for non-configured tool, got %q", got)
	}
}

func TestDisabledEngineIsNoop(t *testing.T) {
	e, _ := New(Config{})
	id := &auth.Identity{} // no acr, no claims — would normally fail
	if err := e.Check("cli_execute", id); err != nil {
		t.Errorf("disabled engine must not fail: %v", err)
	}
}

// wrapErr for the errors.As unwrap test.
type wrapErr struct{ inner error }

func (w *wrapErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrapErr) Unwrap() error { return w.inner }

func strContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

// tiny indexOf to avoid pulling strings into the test file's imports
// alongside a compile-time-avoidable dependency. Not perf-sensitive.
func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
