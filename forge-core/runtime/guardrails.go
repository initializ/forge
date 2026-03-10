package runtime

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
)

// GuardrailEngine checks inbound and outbound messages against policy rules.
type GuardrailEngine struct {
	scaffold *agentspec.PolicyScaffold
	enforce  bool
	logger   Logger
}

// NewGuardrailEngine creates a GuardrailEngine. If scaffold is nil, a default
// is used. When enforce is true, violations return errors; otherwise they are
// logged as warnings.
func NewGuardrailEngine(scaffold *agentspec.PolicyScaffold, enforce bool, logger Logger) *GuardrailEngine {
	if scaffold == nil {
		scaffold = &agentspec.PolicyScaffold{}
	}
	return &GuardrailEngine{scaffold: scaffold, enforce: enforce, logger: logger}
}

// CheckInbound validates an inbound (user) message against guardrails.
func (g *GuardrailEngine) CheckInbound(msg *a2a.Message) error {
	return g.check(msg, "inbound")
}

// CheckOutbound validates an outbound (agent) message against guardrails.
// Unlike CheckInbound, outbound violations are always handled by redacting
// the offending content rather than blocking the entire response. Blocking
// throws away a potentially useful agent response (e.g., code analysis) over
// a false positive from broad PII/secret patterns matching source code.
func (g *GuardrailEngine) CheckOutbound(msg *a2a.Message) error {
	for i, p := range msg.Parts {
		if p.Kind != a2a.PartKindText || p.Text == "" {
			continue
		}
		text := p.Text
		redacted := false

		for _, gr := range g.scaffold.Guardrails {
			switch gr.Type {
			case "no_secrets":
				for _, re := range secretPatterns {
					if re.MatchString(text) {
						text = re.ReplaceAllString(text, "[REDACTED]")
						redacted = true
					}
				}
			case "no_pii":
				for _, re := range piiPatterns {
					if re.MatchString(text) {
						text = re.ReplaceAllString(text, "[REDACTED]")
						redacted = true
					}
				}
			case "content_filter":
				// Content filter: redact blocked words inline.
				if gr.Config != nil {
					if words, ok := gr.Config["blocked_words"]; ok {
						if list, ok := words.([]any); ok {
							lower := strings.ToLower(text)
							for _, w := range list {
								if s, ok := w.(string); ok && strings.Contains(lower, strings.ToLower(s)) {
									text = strings.ReplaceAll(text, s, "[BLOCKED]")
									redacted = true
								}
							}
						}
					}
				}
			}
		}

		if redacted {
			msg.Parts[i].Text = text
			g.logger.Warn("outbound guardrail redaction applied", map[string]any{
				"direction": "outbound",
			})
		}
	}
	return nil
}

func (g *GuardrailEngine) check(msg *a2a.Message, direction string) error {
	text := extractText(msg)
	if text == "" {
		return nil
	}

	for _, gr := range g.scaffold.Guardrails {
		var err error
		switch gr.Type {
		case "content_filter":
			err = g.checkContentFilter(text, gr)
		case "no_pii":
			if direction == "outbound" {
				err = g.checkNoPII(text)
			}
		case "jailbreak_protection":
			if direction == "inbound" {
				err = g.checkJailbreak(text)
			}
		case "no_secrets":
			if direction == "outbound" {
				err = g.checkNoSecrets(text)
			}
		default:
			continue
		}
		if err != nil {
			if g.enforce {
				return fmt.Errorf("guardrail %s (%s): %w", gr.Type, direction, err)
			}
			g.logger.Warn("guardrail violation", map[string]any{
				"guardrail": gr.Type,
				"direction": direction,
				"detail":    err.Error(),
			})
		}
	}
	return nil
}

func extractText(msg *a2a.Message) string {
	var parts []string
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindText && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, " ")
}

func (g *GuardrailEngine) checkContentFilter(text string, gr agentspec.Guardrail) error {
	// Use blocked words from config, or defaults
	blocked := []string{"BLOCKED_CONTENT"}
	if gr.Config != nil {
		if words, ok := gr.Config["blocked_words"]; ok {
			if list, ok := words.([]any); ok {
				blocked = blocked[:0]
				for _, w := range list {
					if s, ok := w.(string); ok {
						blocked = append(blocked, s)
					}
				}
			}
		}
	}
	lower := strings.ToLower(text)
	for _, word := range blocked {
		if strings.Contains(lower, strings.ToLower(word)) {
			return fmt.Errorf("content filter: blocked word %q detected", word)
		}
	}
	return nil
}

var piiPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`), // email
	regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`),                    // phone
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),                            // SSN
}

func (g *GuardrailEngine) checkNoPII(text string) error {
	for _, re := range piiPatterns {
		if re.MatchString(text) {
			return fmt.Errorf("PII pattern detected: %s", re.String())
		}
	}
	return nil
}

var jailbreakPhrases = []string{
	"ignore previous instructions",
	"ignore all instructions",
	"disregard your instructions",
	"forget your rules",
	"you are now",
	"act as if you have no restrictions",
}

func (g *GuardrailEngine) checkJailbreak(text string) error {
	lower := strings.ToLower(text)
	for _, phrase := range jailbreakPhrases {
		if strings.Contains(lower, phrase) {
			return fmt.Errorf("jailbreak pattern detected: %q", phrase)
		}
	}
	return nil
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9\-]{20,}`),                      // Anthropic API keys
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),                            // OpenAI API keys
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),                            // GitHub PATs
	regexp.MustCompile(`gho_[A-Za-z0-9]{36}`),                            // GitHub OAuth tokens
	regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`),                            // GitHub server tokens
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),                   // GitHub fine-grained PATs
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                               // AWS access key IDs
	regexp.MustCompile(`xoxb-[0-9]{10,}-[A-Za-z0-9-]+`),                  // Slack bot tokens
	regexp.MustCompile(`xoxp-[0-9]{10,}-[A-Za-z0-9-]+`),                  // Slack user tokens
	regexp.MustCompile(`-----BEGIN (RSA|EC|OPENSSH|PRIVATE) .*KEY-----`), // Private keys
	regexp.MustCompile(`[0-9]{8,10}:[A-Za-z0-9_-]{35,}`),                 // Telegram bot tokens
}

func (g *GuardrailEngine) checkNoSecrets(text string) error {
	for _, re := range secretPatterns {
		if re.MatchString(text) {
			return fmt.Errorf("potential secret or credential detected in output")
		}
	}
	return nil
}

// CheckToolOutput scans tool output text against configured guardrails
// (no_secrets and no_pii). Matches are always redacted rather than blocked,
// because tool outputs are internal (sent to the LLM, not the user) and
// blocking would kill the entire agent session. Search tools routinely find
// code containing API key patterns in test files, config examples, etc.
func (g *GuardrailEngine) CheckToolOutput(text string) (string, error) {
	if text == "" {
		return text, nil
	}

	for _, gr := range g.scaffold.Guardrails {
		var patterns []*regexp.Regexp
		switch gr.Type {
		case "no_secrets":
			patterns = secretPatterns
		case "no_pii":
			patterns = piiPatterns
		default:
			continue
		}

		for _, re := range patterns {
			if !re.MatchString(text) {
				continue
			}
			// Always redact tool output instead of blocking. Blocking
			// returns a fatal error that kills the agent session, which
			// is too aggressive for tool output (especially search tools
			// that scan source code containing dummy keys in tests).
			text = re.ReplaceAllString(text, "[REDACTED]")
			g.logger.Warn("guardrail redaction", map[string]any{
				"guardrail": gr.Type,
				"direction": "tool_output",
				"detail":    fmt.Sprintf("pattern %s matched, content redacted", re.String()),
			})
		}
	}
	return text, nil
}
