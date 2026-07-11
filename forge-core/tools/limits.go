package tools

import "context"

// Tool-internal output caps exist to protect the LLM context window from
// bulk. When reversible context compression is enabled, that protection is
// the wrong place to economize: a tool-side cut destroys data BEFORE the
// compression hook can shrink it losslessly (live finding: grep_search's
// 50-match default silently cut the one CrashLoopBackOff row at match #78,
// producing a confidently wrong answer — compression never saw it).
//
// The executor stamps the tool context with WithRelaxedLimits when
// compression is on; cap sites consult RelaxedLimits and scale up. The
// loop's post-hook cap and pre-hook safety ceiling still bound everything
// downstream, so "relaxed" never means "unbounded".

type relaxedLimitsKey struct{}

// WithRelaxedLimits marks the context so tool-internal output caps scale up,
// letting the full output reach the compression layer.
func WithRelaxedLimits(ctx context.Context) context.Context {
	return context.WithValue(ctx, relaxedLimitsKey{}, true)
}

// RelaxedLimits reports whether tool-internal output caps should scale up.
func RelaxedLimits(ctx context.Context) bool {
	v, _ := ctx.Value(relaxedLimitsKey{}).(bool)
	return v
}
