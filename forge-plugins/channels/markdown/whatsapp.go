package markdown

import (
	"regexp"
	"strings"
)

// WhatsApp accepts up to 65536 characters in a text message body, but a wall
// that long is unreadable on a phone and the client truncates it behind a
// "Read more" fold. We split at 4000 — the same threshold Slack uses — so
// each chunk stays a scannable message.
const whatsappBodyLimit = 4000

// ToWhatsAppText converts standard markdown to WhatsApp's formatting subset.
//
// WhatsApp supports only *bold*, _italic_, ~strikethrough~, `inline code` and
// ```fenced code```. There are no headings, no links-with-text, no tables and
// no native lists. Everything else degrades to plain text rather than leaking
// raw markdown syntax at the reader:
//
//	# Heading        → *Heading*
//	- item           → • item
//	[text](url)      → text: url
//	| a | b |        → a — b
//	---              → ──────────
//
// Note the asterisk inversion: markdown **bold** is WhatsApp *bold*, and
// markdown *italic* is WhatsApp _italic_. Bold is therefore converted through
// a placeholder so the italic pass can't reclaim the single asterisks it
// just produced.
func ToWhatsAppText(md string) string {
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	inFence := false

	for _, line := range lines {
		// Fenced code delimiters and bodies pass through verbatim — WhatsApp
		// renders ``` fences natively and the contents must not be rewritten.
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}

		out = append(out, convertWhatsAppBlockLine(line))
	}

	return strings.Join(out, "\n")
}

// convertWhatsAppBlockLine handles block-level elements and inline transforms
// for a single non-fenced line.
func convertWhatsAppBlockLine(line string) string {
	// Table separator rows ("|---|:--:|") carry no content — drop them
	// entirely rather than rendering a row of dashes.
	if tableSepRe.MatchString(line) {
		return ""
	}

	// Table rows: "| a | b |" → "a — b". Done before the inline pass so cell
	// contents are transformed but the pipes themselves are not.
	if tableRowRe.MatchString(line) {
		trimmed := strings.Trim(strings.TrimSpace(line), "|")
		cells := strings.Split(trimmed, "|")
		for i, c := range cells {
			cells[i] = applyWhatsAppInline(strings.TrimSpace(c))
		}
		return strings.Join(cells, " — ")
	}

	// Horizontal rule. WhatsApp has no <hr>, so draw one.
	if hrRe.MatchString(line) {
		return "──────────"
	}

	// Headings: "# Title" → "*Title*". Inline transforms are deliberately NOT
	// applied to the heading text — nesting a *bold* span inside the wrapping
	// asterisks produces unbalanced markers that WhatsApp renders literally.
	// Mirrors convertSlackBlockLine's handling for the same reason.
	if m := headerRe.FindStringSubmatch(line); m != nil {
		return "*" + m[2] + "*"
	}

	// Blockquote. WhatsApp added native "> " quoting; keep the marker.
	if m := blockquoteRe.FindStringSubmatch(line); m != nil {
		return "> " + applyWhatsAppInline(m[1])
	}

	// Ordered list: keep the numbering, which WhatsApp renders natively.
	if m := orderedListRe.FindStringSubmatch(line); m != nil {
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		num := orderedNumRe.FindString(strings.TrimSpace(line))
		return indent + num + " " + applyWhatsAppInline(m[1])
	}

	// Unordered list: "- item" → "• item". A leading "*" would otherwise be
	// read as an unbalanced bold marker. Indentation is preserved so nested
	// lists — common in agent output — keep their shape.
	if m := whatsappBulletRe.FindStringSubmatch(line); m != nil {
		return m[1] + "• " + applyWhatsAppInline(m[2])
	}

	return applyWhatsAppInline(line)
}

// applyWhatsAppInline applies inline markdown transforms for WhatsApp text.
//
// Inline code spans are lifted out first and restored last, so their contents
// are never rewritten — a literal "**" inside `code` must survive intact.
func applyWhatsAppInline(line string) string {
	line, codes := liftInlineCode(line)

	// Images before links: "![alt](url)" shares a suffix with "[text](url)",
	// so running linkRe first would leave a stray "!" behind.
	line = imageRe.ReplaceAllStringFunc(line, func(m string) string {
		g := imageRe.FindStringSubmatch(m)
		return flattenLink(g[1], g[2])
	})
	line = linkRe.ReplaceAllStringFunc(line, func(m string) string {
		g := linkRe.FindStringSubmatch(m)
		return flattenLink(g[1], g[2])
	})

	// Bold: **text** → placeholder, so the italic pass below cannot match the
	// single asterisks this produces. Restored after italic.
	line = boldRe.ReplaceAllStringFunc(line, func(m string) string {
		return "\x01" + boldRe.FindStringSubmatch(m)[1] + "\x02"
	})

	// Strikethrough: ~~text~~ → ~text~
	line = strikethroughRe.ReplaceAllString(line, "~${1}~")

	// Italic: *text* → _text_ (placeholders shield the converted bold spans)
	line = italicRe.ReplaceAllString(line, "_${1}_")

	line = strings.ReplaceAll(line, "\x01", "*")
	line = strings.ReplaceAll(line, "\x02", "*")

	return restoreInlineCode(line, codes)
}

// flattenLink renders a markdown link as WhatsApp-safe text. WhatsApp
// auto-links bare URLs but has no anchor syntax, so the label has to be
// spelled out beside the target. A label identical to (or contained in) the
// URL would just be noise, so the URL alone is emitted.
func flattenLink(text, url string) string {
	text = strings.TrimSpace(text)
	url = strings.TrimSpace(url)
	if text == "" || text == url || strings.Contains(url, text) {
		return url
	}
	return text + ": " + url
}

// liftInlineCode replaces each `...` span with an index sentinel and returns
// the extracted spans. The sentinel uses \x00 delimiters, which cannot occur
// in agent output that has already been through the LLM.
func liftInlineCode(line string) (string, []string) {
	if !strings.Contains(line, "`") {
		return line, nil
	}
	var codes []string
	out := inlineCodeRe.ReplaceAllStringFunc(line, func(m string) string {
		codes = append(codes, m)
		return "\x00" + itoa(len(codes)-1) + "\x00"
	})
	return out, codes
}

// restoreInlineCode substitutes the sentinels planted by liftInlineCode back
// to their original `...` spans.
func restoreInlineCode(line string, codes []string) string {
	for i, c := range codes {
		line = strings.ReplaceAll(line, "\x00"+itoa(i)+"\x00", c)
	}
	return line
}

// SplitMessageWhatsApp splits text into chunks that each fit within the
// WhatsApp readability limit. Prefers paragraph boundaries, then newlines,
// then hard splits — same strategy as the Teams and Slack splitters.
func SplitMessageWhatsApp(text string) []string {
	return SplitMessage(text, whatsappBodyLimit)
}

// StripWhatsAppMention removes a leading "@<name>" or "@<e164>" the sender
// typed to invoke the agent in a group, so the prompt handed to the LLM
// doesn't start with the agent's own handle.
//
// WhatsApp mentions are wire-encoded as the mentioned party's E.164 number
// prefixed with "@" (the client substitutes the display name at render time),
// so both forms are matched. Case-insensitive, leading position only.
func StripWhatsAppMention(text string, handles ...string) string {
	trimmed := strings.TrimSpace(text)
	for _, h := range handles {
		h = strings.TrimSpace(strings.TrimPrefix(h, "@"))
		if h == "" {
			continue
		}
		prefix := "@" + h
		if !strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(prefix)) {
			continue
		}
		stripped := strings.TrimSpace(trimmed[len(prefix):])
		return strings.TrimLeft(stripped, ":, ")
	}
	return text
}

// Regexes specific to the WhatsApp text subset. The markdown package already
// defines headerRe / blockquoteRe / orderedListRe / boldRe / italicRe /
// inlineCodeRe / linkRe / strikethroughRe — reuse those.
var (
	imageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	// whatsappBulletRe allows leading indentation, unlike the package-level
	// bulletRe, so nested bullets survive the conversion. Group 1 is the
	// indent, group 2 the item text.
	whatsappBulletRe = regexp.MustCompile(`^([ \t]*)[*\-]\s+(.+)$`)
	tableRowRe       = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	tableSepRe       = regexp.MustCompile(`^\s*\|[\s:|-]+\|\s*$`)
	hrRe             = regexp.MustCompile(`^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	orderedNumRe     = regexp.MustCompile(`^\d+\.`)
)
