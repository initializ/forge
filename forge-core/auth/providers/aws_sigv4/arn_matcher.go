package aws_sigv4

import (
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
