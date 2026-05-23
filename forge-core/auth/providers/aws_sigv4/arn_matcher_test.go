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
