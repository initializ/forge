package optimizer

import "strings"

// detectClient maps a request's User-Agent to a coarse coding-agent name, so
// stats can attribute usage per client (claude-code, codex, cursor, …). Claude
// Code and the Anthropic SDKs send recognizable UA strings; anything else is
// bucketed by its leading token, or "unknown".
func detectClient(userAgent string) string {
	ua := strings.ToLower(userAgent)
	switch {
	case ua == "":
		return "unknown"
	case strings.Contains(ua, "claude-code") || strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude code"):
		return "claude-code"
	case strings.Contains(ua, "codex"):
		return "codex"
	case strings.Contains(ua, "cursor"):
		return "cursor"
	case strings.Contains(ua, "cline"):
		return "cline"
	case strings.Contains(ua, "aider"):
		return "aider"
	case strings.Contains(ua, "opencode"):
		return "opencode"
	case strings.Contains(ua, "anthropic"):
		return "anthropic-sdk"
	default:
		// Fall back to the leading UA token (e.g. "foo/1.2.3" → "foo").
		if i := strings.IndexAny(ua, "/ "); i > 0 {
			return ua[:i]
		}
		return ua
	}
}
