package runtime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/initializ/forge/forge-core/agentspec"
)

// SkillGuardrailEngine enforces skill-declared deny patterns on command inputs,
// tool outputs, and user prompts. It complements the global GuardrailEngine with
// domain-specific rules authored by skill developers.
type SkillGuardrailEngine struct {
	denyCommands  []compiledCommandFilter
	denyOutput    []compiledOutputFilter
	denyPrompts   []compiledCommandFilter
	denyResponses []compiledCommandFilter
	enforce       bool
	logger        Logger
}

type compiledCommandFilter struct {
	re      *regexp.Regexp
	message string
}

type compiledOutputFilter struct {
	re     *regexp.Regexp
	action string // "block" or "redact"
}

// NewSkillGuardrailEngine creates a SkillGuardrailEngine from aggregated skill rules.
// Invalid regex patterns are skipped with a warning.
func NewSkillGuardrailEngine(rules *agentspec.SkillGuardrailRules, enforce bool, logger Logger) *SkillGuardrailEngine {
	engine := &SkillGuardrailEngine{enforce: enforce, logger: logger}

	if rules == nil {
		return engine
	}

	for _, c := range rules.DenyCommands {
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			logger.Warn("skill guardrail: invalid deny_command regex, skipping", map[string]any{
				"pattern": c.Pattern,
				"error":   err.Error(),
			})
			continue
		}
		engine.denyCommands = append(engine.denyCommands, compiledCommandFilter{
			re:      re,
			message: c.Message,
		})
	}

	for _, o := range rules.DenyOutput {
		re, err := regexp.Compile(o.Pattern)
		if err != nil {
			logger.Warn("skill guardrail: invalid deny_output regex, skipping", map[string]any{
				"pattern": o.Pattern,
				"error":   err.Error(),
			})
			continue
		}
		engine.denyOutput = append(engine.denyOutput, compiledOutputFilter{
			re:     re,
			action: o.Action,
		})
	}

	for _, p := range rules.DenyPrompts {
		re, err := regexp.Compile("(?i)" + p.Pattern)
		if err != nil {
			logger.Warn("skill guardrail: invalid deny_prompt regex, skipping", map[string]any{
				"pattern": p.Pattern,
				"error":   err.Error(),
			})
			continue
		}
		engine.denyPrompts = append(engine.denyPrompts, compiledCommandFilter{
			re:      re,
			message: p.Message,
		})
	}

	for _, r := range rules.DenyResponses {
		re, err := regexp.Compile("(?is)" + r.Pattern)
		if err != nil {
			logger.Warn("skill guardrail: invalid deny_response regex, skipping", map[string]any{
				"pattern": r.Pattern,
				"error":   err.Error(),
			})
			continue
		}
		engine.denyResponses = append(engine.denyResponses, compiledCommandFilter{
			re:      re,
			message: r.Message,
		})
	}

	return engine
}

// CheckCommandInput validates a tool call before execution.
//
// For cli_execute, the JSON is parsed to build a "binary arg1 arg2..."
// canonical string so operators can author shell-style patterns
// (`git.*--force`). For any other tool, the raw tool-input JSON is
// used as the match target — so `deny_commands` can match on payload
// fragments of http_request / mcp / builtin tool calls too. This
// generalizes MODIFY/DENY beyond the cli_execute-only path called
// out in governance R4a (#209).
func (s *SkillGuardrailEngine) CheckCommandInput(toolName, toolInput string) error {
	if len(s.denyCommands) == 0 {
		return nil
	}

	matchTarget := canonicalizeToolInput(toolName, toolInput)
	if matchTarget == "" {
		return nil
	}

	for _, f := range s.denyCommands {
		if f.re.MatchString(matchTarget) {
			msg := f.message
			if msg == "" {
				msg = "command blocked by skill guardrail"
			}
			if s.enforce {
				return fmt.Errorf("skill guardrail: %s", msg)
			}
			s.logger.Warn("skill guardrail command match", map[string]any{
				"pattern": f.re.String(),
				"tool":    toolName,
				"target":  matchTarget,
				"message": msg,
			})
			return fmt.Errorf("skill guardrail: %s", msg)
		}
	}

	return nil
}

// CheckCommandOutput validates tool output after execution.
//
// Applies deny_output patterns to the output of ANY tool — not just
// cli_execute. Governance R4a (#209) requires MODIFY (redact) to be
// available for all tool outputs, not a special-cased path. The
// pattern-matching loop is delegated to applyOutputPolicy so other
// call sites (LLM response scanning, future MCP tool result hook)
// can reuse the same block/redact semantics.
func (s *SkillGuardrailEngine) CheckCommandOutput(toolName, toolOutput string) (string, error) {
	if len(s.denyOutput) == 0 || toolOutput == "" {
		return toolOutput, nil
	}
	result := applyOutputPolicy(toolOutput, s.denyOutput, s.logger, toolName)
	switch result.Decision {
	case DecisionDeny:
		return "", fmt.Errorf("tool output blocked by skill guardrail")
	case DecisionModify:
		return result.Modified, nil
	default:
		return toolOutput, nil
	}
}

// applyOutputPolicy runs the block/redact pattern chain over content
// and returns the resulting PolicyResult. Hoisted out of
// CheckCommandOutput for R4a (#209) so future call sites (LLM output
// scanning, RAG-context scanning, MCP tool-result scanning) can share
// the same MODIFY semantics.
//
// Two-pass evaluation (fixes the redact-hides-block downgrade
// reviewer initializ-mk flagged): pass 1 checks every "block" pattern
// against the ORIGINAL content and short-circuits to Deny on any
// match. Only after all blocks pass does pass 2 apply "redact"
// substitutions cumulatively. This guarantees a block-worthy string
// isn't silently downgraded to Modify by an earlier redact that
// rewrote the substring the block pattern would have caught.
//
// `logger` and `tool` are for the redaction log line — pass "" for
// tool when the caller isn't tool-scoped.
func applyOutputPolicy(content string, filters []compiledOutputFilter, logger Logger, tool string) PolicyResult {
	// Pass 1: block checks against ORIGINAL content.
	for _, f := range filters {
		if f.action == "block" && f.re.MatchString(content) {
			return Deny("output matched deny_output block pattern")
		}
	}
	// Pass 2: cumulative redacts.
	modified := content
	changed := false
	for _, f := range filters {
		if f.action != "redact" || !f.re.MatchString(modified) {
			continue
		}
		modified = f.re.ReplaceAllString(modified, "[BLOCKED BY POLICY]")
		changed = true
		fields := map[string]any{
			"pattern": f.re.String(),
			"action":  "redact",
		}
		if tool != "" {
			fields["tool"] = tool
		}
		logger.Warn("skill guardrail output redaction", fields)
	}
	if changed {
		return Modify(modified, "output matched deny_output redact pattern")
	}
	return Allow()
}

// canonicalizeToolInput returns the string against which deny_commands
// patterns are matched. For cli_execute this reconstructs the shell
// command line ("binary args..."); for any other tool the match target is
// the raw JSON PLUS the decoded string scalar values it contains.
//
// Including the decoded values closes a JSON-escape evasion (#238 review):
// in raw JSON a tab is the two-char sequence `\t`, a newline `\n`, a space
// ` ` — so `{"cmd":"kubectl\tdelete pod"}`, which the downstream tool
// decodes and runs as `kubectl<TAB>delete`, would NOT match a
// whitespace-sensitive pattern like `kubectl\s+delete` against the raw JSON
// alone (`\s` never sees a real whitespace byte). json.Unmarshal resolves
// those escapes, so the decoded values carry real whitespace and the pattern
// fires. Raw JSON is still included so payload-shape patterns (matching keys
// / structure) keep working; matching EITHER blocks, which is the safe
// direction for a deny control. cli_execute is unaffected — its target is
// the already-unescaped reconstructed command line.
func canonicalizeToolInput(toolName, toolInput string) string {
	if toolName == "cli_execute" {
		return extractCommandLine(toolInput)
	}
	decoded := decodeJSONStringValues(toolInput)
	if decoded == "" {
		return toolInput
	}
	return toolInput + "\n" + decoded
}

// decodeJSONStringValues parses toolInput as JSON and returns every string
// scalar value it contains (recursively, values only — not keys), joined by
// newlines. json.Unmarshal resolves JSON escapes, so the result carries the
// real bytes the downstream tool will act on. Returns "" when toolInput is
// not valid JSON (the caller then matches the raw input as-is).
func decodeJSONStringValues(toolInput string) string {
	var v any
	if err := json.Unmarshal([]byte(toolInput), &v); err != nil {
		return ""
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return strings.Join(out, "\n")
}

// CheckUserInput validates a user message against deny_prompts patterns.
// Returns an error with the skill-defined redirect message if the prompt matches.
func (s *SkillGuardrailEngine) CheckUserInput(text string) error {
	if len(s.denyPrompts) == 0 || text == "" {
		return nil
	}

	for _, f := range s.denyPrompts {
		if f.re.MatchString(text) {
			msg := f.message
			if msg == "" {
				msg = "prompt blocked by skill guardrail"
			}
			s.logger.Warn("skill guardrail prompt match", map[string]any{
				"pattern": f.re.String(),
				"message": msg,
			})
			return fmt.Errorf("skill guardrail: %s", msg)
		}
	}

	return nil
}

// CheckLLMResponse validates the LLM's response text against deny_responses
// patterns. When a match is found, the response is replaced with the
// skill-defined redirect message to prevent binary/tool enumeration leaks.
// Returns the (possibly replaced) text and whether a replacement occurred.
func (s *SkillGuardrailEngine) CheckLLMResponse(text string) (string, bool) {
	if len(s.denyResponses) == 0 || text == "" {
		return text, false
	}

	for _, f := range s.denyResponses {
		if f.re.MatchString(text) {
			msg := f.message
			if msg == "" {
				msg = "I can help you with specific tasks. What would you like to do?"
			}
			s.logger.Warn("skill guardrail response match", map[string]any{
				"pattern": f.re.String(),
				"action":  "replace",
			})
			return msg, true
		}
	}

	return text, false
}

// extractCommandLine parses the cli_execute tool input JSON to build a command
// line string "binary arg1 arg2 ..." for pattern matching.
func extractCommandLine(toolInput string) string {
	var input struct {
		Binary string   `json:"binary"`
		Args   []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(toolInput), &input); err != nil {
		return ""
	}
	if input.Binary == "" {
		return ""
	}

	parts := []string{input.Binary}
	parts = append(parts, input.Args...)
	return strings.Join(parts, " ")
}
