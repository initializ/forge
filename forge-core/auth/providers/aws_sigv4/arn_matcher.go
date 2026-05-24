package aws_sigv4

import (
	"errors"
	"fmt"
	"path"
)

// ArnMatcher checks a caller's ARN against a list of shell-style globs.
// Empty list = allow any IAM principal. Invalid patterns fail at startup,
// never at request time.
//
// Decision §9.3: shell-style globs via path.Match. Supports * and ? but
// NOT regex syntax — simpler mental model, less footgun.
type ArnMatcher struct {
	patterns []string
}

// NewArnMatcher validates each pattern via path.Match("", pattern). A
// malformed pattern is a config bug; we want it surfaced at Factory time.
func NewArnMatcher(patterns []string) (*ArnMatcher, error) {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("invalid glob %q: %w", p, err)
		}
	}
	return &ArnMatcher{patterns: patterns}, nil
}

// Match returns true if the ARN matches any pattern, or if the matcher
// has no patterns (which means "allow any principal").
//
// NOTE: STS GetCallerIdentity returns the ASSUMED-ROLE ARN form
// ("arn:aws:sts::ACCOUNT:assumed-role/RoleName/SessionName"), not the
// role's own ARN ("arn:aws:iam::ACCOUNT:role/RoleName"). Operators must
// write patterns against the assumed-role form — PR6 docs spell this out.
func (m *ArnMatcher) Match(arn string) bool {
	if len(m.patterns) == 0 {
		return true
	}
	for _, p := range m.patterns {
		if ok, _ := path.Match(p, arn); ok {
			return true
		}
	}
	return false
}

// validateAccountID checks for an AWS account ID — 12 ASCII digits.
// Catches typos (region names, role ARNs pasted by mistake) at Factory
// time so a misconfigured allowed_accounts entry doesn't silently
// become an unreachable pattern.
func validateAccountID(s string) error {
	if len(s) != 12 {
		return fmt.Errorf("account ID %q: expected 12 digits, got %d chars", s, len(s))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return errors.New("account ID must be 12 digits")
		}
	}
	return nil
}

// expandAccountGlobs returns the canonical ARN glob set that covers
// every STS identity shape in a given account. The four shapes:
//
//	arn:aws:iam::<acct>:user/<name>           — direct IAM user
//	arn:aws:iam::<acct>:role/<name>           — direct IAM role (rare; usually only EC2/Lambda)
//	arn:aws:sts::<acct>:assumed-role/<role>/<session>  — SSO, AssumeRole, IRSA
//	arn:aws:sts::<acct>:federated-user/<name> — SAML/web-identity federation
//
// path.Match's `*` doesn't cross `/`, so the assumed-role glob needs
// two `*` segments to span both RoleName and SessionName.
func expandAccountGlobs(acct string) []string {
	return []string{
		"arn:aws:iam::" + acct + ":user/*",
		"arn:aws:iam::" + acct + ":role/*",
		"arn:aws:sts::" + acct + ":assumed-role/*/*",
		"arn:aws:sts::" + acct + ":federated-user/*",
	}
}
