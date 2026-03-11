package runtime

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

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
				for _, p := range piiPatterns {
					matches := p.re.FindAllString(text, -1)
					for _, m := range matches {
						if p.validate != nil && !p.validate(m) {
							continue
						}
						text = strings.ReplaceAll(text, m, "[REDACTED]")
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

// piiCheckerPattern pairs a regex with an optional validator function.
// When a validator is present, regex matches are only considered true positives
// if the validator confirms the matched text (e.g., Luhn check for credit cards,
// structure validation for SSNs). This follows the pattern from the reference
// guardrails library to reduce false positives.
type piiCheckerPattern struct {
	re       *regexp.Regexp
	validate func(string) bool // nil means regex match alone is sufficient
}

// Credit card regex: Visa, Mastercard, Amex, Discover with optional separators.
var ccRegex = `\b(?:` +
	`4[0-9]{3}[\s-]?[0-9]{4}[\s-]?[0-9]{4}[\s-]?[0-9]{1,4}|` + // Visa
	`(?:5[1-5][0-9]{2}|222[1-9]|22[3-9][0-9]|2[3-6][0-9]{2}|27[01][0-9]|2720)[\s-]?[0-9]{4}[\s-]?[0-9]{4}[\s-]?[0-9]{4}|` + // Mastercard
	`3[47][0-9]{2}[\s-]?[0-9]{6}[\s-]?[0-9]{5}|` + // Amex
	`(?:6011|65[0-9]{2}|64[4-9][0-9])[\s-]?[0-9]{4}[\s-]?[0-9]{4}[\s-]?[0-9]{4}` + // Discover
	`)\b`

var piiPatterns = []piiCheckerPattern{
	{re: regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)},          // email
	{re: regexp.MustCompile(`\b(?:\+?1[-.\s])?\(?[2-9]\d{2}\)?[-.\s]\d{3}[-.\s]\d{4}\b`)}, // phone (area code 2-9, separators required)
	{re: regexp.MustCompile(`\b\d{3}[-.\s]?\d{2}[-.\s]?\d{4}\b`), validate: validateSSN},  // SSN with structural validation
	{re: regexp.MustCompile(ccRegex), validate: validateLuhn},                             // credit card with Luhn check
}

func (g *GuardrailEngine) checkNoPII(text string) error {
	for _, p := range piiPatterns {
		matches := p.re.FindAllString(text, -1)
		for _, m := range matches {
			if p.validate != nil && !p.validate(m) {
				continue
			}
			return fmt.Errorf("PII pattern detected: %s", p.re.String())
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
		switch gr.Type {
		case "no_secrets":
			for _, re := range secretPatterns {
				if !re.MatchString(text) {
					continue
				}
				if g.enforce {
					return "", fmt.Errorf("tool output blocked by content policy")
				}
				text = re.ReplaceAllString(text, "[REDACTED]")
				g.logger.Warn("guardrail redaction", map[string]any{
					"guardrail": gr.Type,
					"direction": "tool_output",
					"detail":    fmt.Sprintf("pattern %s matched, content redacted", re.String()),
				})
			}
		case "no_pii":
			for _, p := range piiPatterns {
				if !p.re.MatchString(text) {
					continue
				}
				// Check if any match passes validation
				hasValidMatch := false
				if p.validate == nil {
					hasValidMatch = true
				} else {
					for _, m := range p.re.FindAllString(text, -1) {
						if p.validate(m) {
							hasValidMatch = true
							break
						}
					}
				}
				if !hasValidMatch {
					continue
				}
				if g.enforce {
					return "", fmt.Errorf("tool output blocked by content policy")
				}
				// Warn mode: redact only validated matches
				if p.validate != nil {
					v := p.validate // capture for closure
					text = p.re.ReplaceAllStringFunc(text, func(s string) string {
						if v(s) {
							return "[REDACTED]"
						}
						return s
					})
				} else {
					text = p.re.ReplaceAllString(text, "[REDACTED]")
				}
				g.logger.Warn("guardrail redaction", map[string]any{
					"guardrail": gr.Type,
					"direction": "tool_output",
					"detail":    fmt.Sprintf("pattern %s matched, content redacted", p.re.String()),
				})
			}
		default:
			continue
		}
	}
	return text, nil
}

// --- PII Validators ---
// Ported from the reference guardrails library to reduce false positives.

// validateSSN validates a US Social Security Number structure.
// Rejects area=000/666/900+, group=00, serial=0000, all-same digits, and known test SSNs.
func validateSSN(s string) bool {
	cleaned := strings.NewReplacer("-", "", " ", "", ".", "").Replace(s)
	if len(cleaned) != 9 {
		return false
	}
	for _, r := range cleaned {
		if !unicode.IsDigit(r) {
			return false
		}
	}

	area := cleaned[0:3]
	group := cleaned[3:5]
	serial := cleaned[5:9]

	if area == "000" || area == "666" || area[0] == '9' {
		return false
	}
	if group == "00" {
		return false
	}
	if serial == "0000" {
		return false
	}

	// All same digits
	allSame := true
	for i := 1; i < len(cleaned); i++ {
		if cleaned[i] != cleaned[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return false
	}

	// Known test/advertising SSNs
	testSSNs := map[string]bool{
		"078051120": true,
		"219099999": true,
		"123456789": true,
	}
	return !testSSNs[cleaned]
}

// validateLuhn performs Luhn checksum validation on a credit card number.
// Strips separators (spaces, dashes) before validating.
func validateLuhn(s string) bool {
	cleaned := strings.NewReplacer(" ", "", "-", "").Replace(s)
	if len(cleaned) < 13 || len(cleaned) > 19 {
		return false
	}
	for _, r := range cleaned {
		if !unicode.IsDigit(r) {
			return false
		}
	}

	sum := 0
	double := false
	for i := len(cleaned) - 1; i >= 0; i-- {
		digit := int(cleaned[i] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}
