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

// CheckCommandInput validates a tool call before execution. It only fires for
// cli_execute tool calls. Returns an error if the command matches a deny pattern.
func (s *SkillGuardrailEngine) CheckCommandInput(toolName, toolInput string) error {
	if toolName != "cli_execute" {
		return nil
	}
	if len(s.denyCommands) == 0 {
		return nil
	}

	cmdLine := extractCommandLine(toolInput)
	if cmdLine == "" {
		return nil
	}

	for _, f := range s.denyCommands {
		if f.re.MatchString(cmdLine) {
			msg := f.message
			if msg == "" {
				msg = "command blocked by skill guardrail"
			}
			if s.enforce {
				return fmt.Errorf("skill guardrail: %s", msg)
			}
			s.logger.Warn("skill guardrail command match", map[string]any{
				"pattern": f.re.String(),
				"command": cmdLine,
				"message": msg,
			})
			return fmt.Errorf("skill guardrail: %s", msg)
		}
	}

	return nil
}

// CheckCommandOutput validates tool output after execution. It only fires for
// cli_execute tool calls. Returns the (possibly redacted) output and an error
// if the output matches a "block" pattern.
func (s *SkillGuardrailEngine) CheckCommandOutput(toolName, toolOutput string) (string, error) {
	if toolName != "cli_execute" {
		return toolOutput, nil
	}
	if len(s.denyOutput) == 0 || toolOutput == "" {
		return toolOutput, nil
	}

	for _, f := range s.denyOutput {
		if !f.re.MatchString(toolOutput) {
			continue
		}

		switch f.action {
		case "block":
			return "", fmt.Errorf("tool output blocked by skill guardrail")
		case "redact":
			toolOutput = f.re.ReplaceAllString(toolOutput, "[BLOCKED BY POLICY]")
			s.logger.Warn("skill guardrail output redaction", map[string]any{
				"pattern": f.re.String(),
				"action":  "redact",
			})
		}
	}

	return toolOutput, nil
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
