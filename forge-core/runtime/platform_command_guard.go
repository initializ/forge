package runtime

import (
	"fmt"
	"regexp"
)

// PlatformCommandSpec is one operator-authored command-deny pattern from
// the platform policy layers (#238), before compilation. The Layer* fields
// carry attribution so a runtime block can name the policy that forbade the
// command in the audit stream.
type PlatformCommandSpec struct {
	Pattern     string
	Message     string
	LayerSource string // "system" / "user" / "workspace"
	LayerPath   string
}

// platformCommandPattern is a compiled PlatformCommandSpec.
type platformCommandPattern struct {
	re   *regexp.Regexp
	spec PlatformCommandSpec
}

// PlatformCommandMatch is returned by PlatformCommandGuard.Match when a tool
// call's arguments match an operator pattern. It names the offending pattern,
// the operator's custom message (if any), and the first-denying layer.
type PlatformCommandMatch struct {
	Pattern     string
	Message     string
	LayerSource string
	LayerPath   string
}

// PlatformCommandGuard enforces operator-authored command-deny patterns on
// EVERY tool call, regardless of the active skill (#238 / ASI02). It is the
// platform-layer sibling of SkillGuardrailEngine's deny_commands and reuses
// the same match-target semantics (canonicalizeToolInput): cli_execute → the
// reconstructed command line, any other tool → the raw tool-input JSON.
//
// Unlike NewSkillGuardrailEngine, which SKIPS an invalid regex with a warning,
// this guard fails closed: NewPlatformCommandGuard returns an error on the
// first uncompilable pattern so the runner can abort startup — an operator
// policy must never silently drop a rule.
type PlatformCommandGuard struct {
	patterns []platformCommandPattern
}

// NewPlatformCommandGuard compiles the resolved (unioned) platform command
// specs. It returns an error naming the offending pattern + layer on the
// first regex that fails to compile, so startup aborts loudly rather than
// enforcing a partial policy. A nil/empty spec slice yields an empty guard.
func NewPlatformCommandGuard(specs []PlatformCommandSpec) (*PlatformCommandGuard, error) {
	g := &PlatformCommandGuard{}
	for _, s := range specs {
		if s.Pattern == "" {
			continue
		}
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid denied_command_patterns regex %q (layer %s, %s): %w",
				s.Pattern, s.LayerSource, s.LayerPath, err)
		}
		g.patterns = append(g.patterns, platformCommandPattern{re: re, spec: s})
	}
	return g, nil
}

// Empty reports whether the guard has no patterns (nil-safe) — lets the
// runner skip registering the BeforeToolExec hook entirely.
func (g *PlatformCommandGuard) Empty() bool {
	return g == nil || len(g.patterns) == 0
}

// Match returns the first pattern (in layer order) whose regex matches the
// canonicalized tool input, or nil when the call is allowed. The match target
// is identical to skill deny_commands so operators and skill authors author
// patterns the same way.
func (g *PlatformCommandGuard) Match(toolName, toolInput string) *PlatformCommandMatch {
	if g.Empty() {
		return nil
	}
	target := canonicalizeToolInput(toolName, toolInput)
	if target == "" {
		return nil
	}
	for i := range g.patterns {
		if g.patterns[i].re.MatchString(target) {
			s := g.patterns[i].spec
			return &PlatformCommandMatch{
				Pattern:     s.Pattern,
				Message:     s.Message,
				LayerSource: s.LayerSource,
				LayerPath:   s.LayerPath,
			}
		}
	}
	return nil
}
