// Package markdown converts standard markdown to platform-specific formats.
package markdown

import (
	"regexp"
	"strings"
)

// ToTelegramHTML converts standard markdown to Telegram-compatible HTML.
func ToTelegramHTML(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false
	var codeLang string
	var codeLines []string

	for _, line := range lines {
		// Check for fenced code block delimiters
		if rest, ok := strings.CutPrefix(line, "```"); ok {
			if !inCodeBlock {
				inCodeBlock = true
				codeLang = strings.TrimSpace(rest)
				codeLines = nil
				continue
			}
			// Closing code block
			inCodeBlock = false
			code := escapeHTML(strings.Join(codeLines, "\n"))
			if codeLang != "" {
				result = append(result, `<pre><code class="language-`+codeLang+`">`+code+"</code></pre>")
			} else {
				result = append(result, "<pre><code>"+code+"</code></pre>")
			}
			codeLang = ""
			codeLines = nil
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Block-level transforms on non-code lines
		line = convertTelegramBlockLine(line)
		result = append(result, line)
	}

	// If code block was never closed, flush remaining lines as code
	if inCodeBlock {
		code := escapeHTML(strings.Join(codeLines, "\n"))
		if codeLang != "" {
			result = append(result, `<pre><code class="language-`+codeLang+`">`+code+"</code></pre>")
		} else {
			result = append(result, "<pre><code>"+code+"</code></pre>")
		}
	}

	return strings.Join(result, "\n")
}

// convertTelegramBlockLine handles block-level elements and inline transforms for a single line.
func convertTelegramBlockLine(line string) string {
	// Headers: # Header → <b>Header</b>
	if m := headerRe.FindStringSubmatch(line); m != nil {
		return "<b>" + escapeHTML(m[2]) + "</b>"
	}

	// Blockquotes: > text → <blockquote>text</blockquote>
	if m := blockquoteRe.FindStringSubmatch(line); m != nil {
		inner := escapeHTML(m[1])
		inner = applyTelegramInline(inner)
		return "<blockquote>" + inner + "</blockquote>"
	}

	// Bullet lists: - item or * item → • item
	if m := bulletRe.FindStringSubmatch(line); m != nil {
		inner := escapeHTML(m[1])
		inner = applyTelegramInline(inner)
		return "• " + inner
	}

	// Regular line: escape HTML, then apply inline transforms
	line = escapeHTML(line)
	line = applyTelegramInline(line)
	return line
}

// applyTelegramInline applies inline markdown transforms for Telegram HTML.
// Input must already be HTML-escaped.
func applyTelegramInline(line string) string {
	// Inline code: `code` → <code>code</code> (process first to protect contents)
	line = inlineCodeRe.ReplaceAllString(line, "<code>$1</code>")

	// Bold: **text** → <b>text</b>
	line = boldRe.ReplaceAllString(line, "<b>$1</b>")

	// Strikethrough: ~~text~~ → <s>text</s>
	line = strikethroughRe.ReplaceAllString(line, "<s>$1</s>")

	// Italic: *text* → <i>text</i>
	line = italicRe.ReplaceAllString(line, "<i>$1</i>")

	// Links: [text](url) → <a href="url">text</a>
	line = linkRe.ReplaceAllString(line, `<a href="$2">$1</a>`)

	return line
}

// ToSlackMrkdwn converts standard markdown to Slack mrkdwn format.
func ToSlackMrkdwn(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false
	var codeLines []string

	for _, line := range lines {
		// Check for fenced code block delimiters
		if _, ok := strings.CutPrefix(line, "```"); ok {
			if !inCodeBlock {
				inCodeBlock = true
				codeLines = nil
				continue
			}
			// Closing code block — strip language hint
			inCodeBlock = false
			result = append(result, "```\n"+strings.Join(codeLines, "\n")+"\n```")
			codeLines = nil
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		// Block-level transforms on non-code lines
		line = convertSlackBlockLine(line)
		result = append(result, line)
	}

	// If code block was never closed, flush remaining lines
	if inCodeBlock {
		result = append(result, "```\n"+strings.Join(codeLines, "\n")+"\n```")
	}

	return strings.Join(result, "\n")
}

// convertSlackBlockLine handles block-level elements and inline transforms for a single line.
func convertSlackBlockLine(line string) string {
	// Headers: # Header → *Header*
	if m := headerRe.FindStringSubmatch(line); m != nil {
		return "*" + m[2] + "*"
	}

	// Blockquotes: > text → > text (Slack supports this natively)
	if m := blockquoteRe.FindStringSubmatch(line); m != nil {
		inner := applySlackInline(m[1])
		return "> " + inner
	}

	// Bullet lists: - item or * item → • item (avoid conflict with Slack bold *)
	if m := bulletRe.FindStringSubmatch(line); m != nil {
		inner := applySlackInline(m[1])
		return "• " + inner
	}

	// Regular line: apply inline transforms
	line = applySlackInline(line)
	return line
}

// applySlackInline applies inline markdown transforms for Slack mrkdwn.
func applySlackInline(line string) string {
	// Bold: **text** → placeholder \x01text\x02 to protect from italic regex
	line = boldRe.ReplaceAllStringFunc(line, func(m string) string {
		inner := boldRe.FindStringSubmatch(m)[1]
		return "\x01" + inner + "\x02"
	})

	// Strikethrough: ~~text~~ → ~text~
	line = strikethroughRe.ReplaceAllString(line, "~${1}~")

	// Italic: *text* → _text_ (won't match \x01..\x02 placeholders)
	line = italicRe.ReplaceAllString(line, "_${1}_")

	// Restore bold placeholders → *text*
	line = strings.ReplaceAll(line, "\x01", "*")
	line = strings.ReplaceAll(line, "\x02", "*")

	// Links: [text](url) → <url|text>
	line = linkRe.ReplaceAllString(line, "<$2|$1>")

	return line
}

// SplitMessage splits a long message into chunks that fit within limit.
// It splits at paragraph boundaries first, then newlines, then hard-splits.
func SplitMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= limit {
			chunks = append(chunks, remaining)
			break
		}

		chunk := remaining[:limit]

		// Try to split at paragraph boundary (\n\n)
		if idx := strings.LastIndex(chunk, "\n\n"); idx > 0 {
			chunks = append(chunks, remaining[:idx])
			remaining = remaining[idx+2:]
			continue
		}

		// Try to split at newline
		if idx := strings.LastIndex(chunk, "\n"); idx > 0 {
			chunks = append(chunks, remaining[:idx])
			remaining = remaining[idx+1:]
			continue
		}

		// Hard split at limit
		chunks = append(chunks, chunk)
		remaining = remaining[limit:]
	}

	return chunks
}

// escapeHTML escapes special HTML characters.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// Compiled regexes for inline markdown patterns.
var (
	headerRe        = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	blockquoteRe    = regexp.MustCompile(`^>\s?(.*)$`)
	bulletRe        = regexp.MustCompile(`^[\*\-]\s+(.+)$`)
	boldRe          = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicRe        = regexp.MustCompile(`\*(.+?)\*`)
	inlineCodeRe    = regexp.MustCompile("`([^`]+)`")
	linkRe          = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	strikethroughRe = regexp.MustCompile(`~~(.+?)~~`)
)
