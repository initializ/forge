package aws_sigv4

import (
	"testing"
)

func TestArnMatcher_EmptyAllowsAll(t *testing.T) {
	m, err := NewArnMatcher(nil)
	if err != nil {
		t.Fatalf("NewArnMatcher: %v", err)
	}
	if !m.Match("arn:aws:iam::123:role/anything") {
		t.Error("empty matcher should allow any ARN")
	}
}

func TestArnMatcher_Glob(t *testing.T) {
	m, err := NewArnMatcher([]string{
		"arn:aws:iam::123:role/forge-*",
		"arn:aws:sts::123:assumed-role/ci-deploy/*",
	})
	if err != nil {
		t.Fatalf("NewArnMatcher: %v", err)
	}
	cases := map[string]bool{
		"arn:aws:iam::123:role/forge-deploy":                   true,
		"arn:aws:iam::123:role/forge-runner":                   true,
		"arn:aws:iam::123:role/forge":                          false,
		"arn:aws:iam::123:role/other-deploy":                   false,
		"arn:aws:iam::456:role/forge-deploy":                   false,
		"arn:aws:sts::123:assumed-role/ci-deploy/session-1":    true,
		"arn:aws:sts::123:assumed-role/ci-deploy/another-sess": true,
		"arn:aws:sts::123:assumed-role/wrong-role/session-1":   false,
	}
	for in, want := range cases {
		if got := m.Match(in); got != want {
			t.Errorf("Match(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestArnMatcher_InvalidPatternFailsAtStartup(t *testing.T) {
	if _, err := NewArnMatcher([]string{"["}); err == nil {
		t.Fatal("expected error for malformed glob")
	}
}

func TestArnMatcher_AssumedRoleNeedsTwoStars(t *testing.T) {
	// Reminder for operators: STS returns the assumed-role ARN, not the
	// IAM role ARN. A pattern that matches "arn:aws:iam::ACCT:role/X-*"
	// will NOT match "arn:aws:sts::ACCT:assumed-role/X-foo/session".
	// PR6 docs spell this out.
	m, _ := NewArnMatcher([]string{"arn:aws:iam::123:role/forge-*"})
	if m.Match("arn:aws:sts::123:assumed-role/forge-deploy/session") {
		t.Error("IAM role pattern should NOT match STS assumed-role ARN — docs invariant")
	}
}

func TestValidateAccountID(t *testing.T) {
	good := []string{"412664885516", "000000000000", "999999999999"}
	for _, s := range good {
		if err := validateAccountID(s); err != nil {
			t.Errorf("validateAccountID(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{
		"",                // empty
		"1234",            // too short
		"4126648855161",   // 13 chars
		"us-east-1",       // not digits
		"arn:aws:iam::1:", // ARN form
		"412 664885516",   // space
	}
	for _, s := range bad {
		if err := validateAccountID(s); err == nil {
			t.Errorf("validateAccountID(%q) returned nil, want error", s)
		}
	}
}

func TestExpandAccountGlobs(t *testing.T) {
	got := expandAccountGlobs("412664885516")
	want := map[string]bool{
		"arn:aws:iam::412664885516:user/*":           true,
		"arn:aws:iam::412664885516:role/*":           true,
		"arn:aws:sts::412664885516:assumed-role/*/*": true,
		"arn:aws:sts::412664885516:federated-user/*": true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d patterns, want %d", len(got), len(want))
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected pattern %q", g)
		}
	}
}

// End-to-end: each expanded pattern matches the realistic ARN it's
// supposed to cover.
func TestExpandedAccountPatterns_MatchRealisticARNs(t *testing.T) {
	m, err := NewArnMatcher(expandAccountGlobs("412664885516"))
	if err != nil {
		t.Fatalf("NewArnMatcher: %v", err)
	}
	cases := map[string]bool{
		// Should match (in-account):
		"arn:aws:iam::412664885516:user/alice":                                             true,
		"arn:aws:iam::412664885516:role/ec2-instance-role":                                 true,
		"arn:aws:sts::412664885516:assumed-role/AWSReservedSSO_PowerUserAccess_abc/naveen": true,
		"arn:aws:sts::412664885516:assumed-role/ci-deploy/session-id":                      true,
		"arn:aws:sts::412664885516:federated-user/saml-jane":                               true,
		// Should NOT match (different account):
		"arn:aws:iam::999999999999:user/eve":                      false,
		"arn:aws:sts::999999999999:assumed-role/SomeRole/session": false,
	}
	for arn, want := range cases {
		if got := m.Match(arn); got != want {
			t.Errorf("Match(%q) = %v, want %v", arn, got, want)
		}
	}
}
