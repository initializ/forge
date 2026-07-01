package runtime

import (
	"regexp"
	"testing"
)

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
