package runtime

import (
	"regexp"
	"testing"
)

// TestPolicyDecision_SeverityOrdering pins the ordinal-as-severity
// contract callers rely on (LibraryGuardrailEngine.CheckOutbound
// uses `>` to escalate the aggregate result). Reordering these
// constants without updating aggregate sites is exactly the class
// of silent breakage this test guards against.
func TestPolicyDecision_SeverityOrdering(t *testing.T) {
	order := []PolicyDecision{
		DecisionAllow,
		DecisionModify,
		DecisionStepUp,
		DecisionDefer,
		DecisionDeny,
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] {
			t.Errorf("severity broken: %s(%d) should be < %s(%d)",
				order[i-1], order[i-1], order[i], order[i])
		}
	}
	// Also pin the zero-value expectation — deployments that
	// return `PolicyResult{}` implicitly mean Allow.
	if DecisionAllow != 0 {
		t.Errorf("DecisionAllow must remain the zero value; got %d", DecisionAllow)
	}
}

func TestPolicyDecision_StringMapping(t *testing.T) {
	cases := []struct {
		d    PolicyDecision
		want string
	}{
		{DecisionAllow, "allow"},
		{DecisionDeny, "deny"},
		{DecisionModify, "modify"},
		{DecisionStepUp, "step_up"},
		{DecisionDefer, "defer"},
		{PolicyDecision(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.want {
			t.Errorf("%d.String(): got %q want %q", int(c.d), got, c.want)
		}
	}
}

func TestPolicyResult_Constructors(t *testing.T) {
	if r := Allow(); r.Decision != DecisionAllow || r.Modified != "" || r.Reason != "" {
		t.Errorf("Allow(): unexpected %+v", r)
	}
	if r := Deny("nope"); r.Decision != DecisionDeny || r.Reason != "nope" {
		t.Errorf("Deny(): unexpected %+v", r)
	}
	if r := Modify("redacted", "matched"); r.Decision != DecisionModify || r.Modified != "redacted" || r.Reason != "matched" {
		t.Errorf("Modify(): unexpected %+v", r)
	}
}

// TestApplyOutputPolicy_ModifyForAnyTool proves the R4a acceptance
// criterion: "MODIFY case emitted for a non-cli_execute tool output".
// The helper is content-agnostic — it doesn't know which tool the
// caller was operating on, which is the whole point of R4a.
func TestApplyOutputPolicy_ModifyForAnyTool(t *testing.T) {
	filters := []compiledOutputFilter{{
		re:     regexp.MustCompile(`secret-[a-z]+`),
		action: "redact",
	}}
	res := applyOutputPolicy("prod-db uses secret-abc", filters, &testLogger{}, "http_request")
	if res.Decision != DecisionModify {
		t.Fatalf("expected DecisionModify, got %s", res.Decision)
	}
	if res.Modified != "prod-db uses [BLOCKED BY POLICY]" {
		t.Errorf("unexpected modified content: %q", res.Modified)
	}
}

func TestApplyOutputPolicy_BlockShortCircuits(t *testing.T) {
	filters := []compiledOutputFilter{
		{re: regexp.MustCompile(`redact-me`), action: "redact"},
		{re: regexp.MustCompile(`BEGIN CERTIFICATE`), action: "block"},
	}
	res := applyOutputPolicy("data: BEGIN CERTIFICATE ... redact-me", filters, &testLogger{}, "web_search")
	if res.Decision != DecisionDeny {
		t.Errorf("expected DecisionDeny on block match, got %s", res.Decision)
	}
}

// TestApplyOutputPolicy_RedactDoesNotHideEarlierBlock is a regression
// test for reviewer initializ-mk's #221 point: a "redact" pattern
// listed before a "block" pattern MUST NOT rewrite text in a way
// that suppresses the block match. Pre-fix, the loop matched
// blocks against the progressively-redacted string, so a
// well-crafted redact-then-block config silently downgraded Deny
// to Modify. Post-fix, block checks run against the ORIGINAL
// content first (two-pass evaluation).
func TestApplyOutputPolicy_RedactDoesNotHideEarlierBlock(t *testing.T) {
	// The redact rule matches "secret " and rewrites it away.
	// The block rule wants to match "secret key" — with the pre-fix
	// single-pass loop it never fires because "secret " was already
	// redacted to "[BLOCKED BY POLICY]". Post-fix, block runs first
	// against original content and returns Deny.
	filters := []compiledOutputFilter{
		{re: regexp.MustCompile(`secret `), action: "redact"},
		{re: regexp.MustCompile(`secret key`), action: "block"},
	}
	res := applyOutputPolicy("leaked secret key: abc", filters, &testLogger{}, "any_tool")
	if res.Decision != DecisionDeny {
		t.Errorf("block must beat earlier redact — got %s (modified=%q)",
			res.Decision, res.Modified)
	}
}

func TestApplyOutputPolicy_AllowWhenNoMatch(t *testing.T) {
	filters := []compiledOutputFilter{{
		re:     regexp.MustCompile(`nope`),
		action: "redact",
	}}
	res := applyOutputPolicy("nothing to see", filters, &testLogger{}, "")
	if res.Decision != DecisionAllow {
		t.Errorf("expected DecisionAllow, got %s (mod=%q)", res.Decision, res.Modified)
	}
}
