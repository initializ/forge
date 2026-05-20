package markdown

import (
	"html"
	"regexp"
	"strings"
)

// Teams body size: Microsoft enforces ~28 KB per chat message body.
// We split at 24 KB to leave headroom for the HTML-tag overhead added by
// MarkdownToTeamsHTML (bold/italic/code/link wrappers can easily double a
// single-line markdown to its HTML equivalent in worst-case inputs).
const teamsBodyLimit = 24000

// TeamsMention is the inbound representation of a Microsoft Graph mention
// entry (mentions[i] in a chatMessage). Only the fields the adapter needs
// are modelled — extras decode silently.
type TeamsMention struct {
	ID        int    `json:"id"`
	Text      string `json:"mentionText"`
	Mentioned struct {
		User struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
	} `json:"mentioned"`
}

// MarkdownToTeamsHTML converts standard markdown to the HTML subset Teams
// renders in chatMessage bodies when contentType == "html".
//
// Supported: headings, bold, italic, inline code, fenced code, links,
// ordered/unordered lists, blockquotes. Unsupported features (e.g. tables,
// images) degrade to escaped plain text rather than raw HTML so the output
// is always safe to drop into body.content.
func MarkdownToTeamsHTML(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	inFence := false
	var fence []string
	var fenceLang string

	flushFence := func() {
		code := html.EscapeString(strings.Join(fence, "\n"))
		if fenceLang != "" {
			out = append(out, `<pre><code class="language-`+fenceLang+`">`+code+"</code></pre>")
		} else {
			out = append(out, "<pre><code>"+code+"</code></pre>")
		}
		fence = nil
		fenceLang = ""
	}

	// List grouping: we collect consecutive list lines into a single <ul>/<ol>.
	var listKind byte // 'u' for ul, 'o' for ol, 0 for none
	var listItems []string

	flushList := func() {
		if len(listItems) == 0 {
			return
		}
		tag := "ul"
		if listKind == 'o' {
			tag = "ol"
		}
		var b strings.Builder
		b.WriteString("<")
		b.WriteString(tag)
		b.WriteString(">")
		for _, item := range listItems {
			b.WriteString("<li>")
			b.WriteString(item)
			b.WriteString("</li>")
		}
		b.WriteString("</")
		b.WriteString(tag)
		b.WriteString(">")
		out = append(out, b.String())
		listItems = nil
		listKind = 0
	}

	for _, line := range lines {
		// Fenced code block delimiters.
		if rest, ok := strings.CutPrefix(line, "```"); ok {
			if !inFence {
				flushList()
				inFence = true
				fenceLang = strings.TrimSpace(rest)
				continue
			}
			inFence = false
			flushFence()
			continue
		}

		if inFence {
			fence = append(fence, line)
			continue
		}

		// Headings (# through ######).
		if m := headerRe.FindStringSubmatch(line); m != nil {
			flushList()
			level := len(m[1])
			out = append(out, "<h"+itoa(level)+">"+html.EscapeString(m[2])+"</h"+itoa(level)+">")
			continue
		}

		// Blockquote.
		if m := blockquoteRe.FindStringSubmatch(line); m != nil {
			flushList()
			out = append(out, "<blockquote>"+applyTeamsInline(html.EscapeString(m[1]))+"</blockquote>")
			continue
		}

		// Ordered list item: "1. text", "2. text", ...
		if m := orderedListRe.FindStringSubmatch(line); m != nil {
			if listKind != 'o' {
				flushList()
				listKind = 'o'
			}
			listItems = append(listItems, applyTeamsInline(html.EscapeString(m[1])))
			continue
		}

		// Unordered list item: "- text" or "* text".
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			if listKind != 'u' {
				flushList()
				listKind = 'u'
			}
			listItems = append(listItems, applyTeamsInline(html.EscapeString(m[1])))
			continue
		}

		// Blank line — flush any open list.
		if strings.TrimSpace(line) == "" {
			flushList()
			out = append(out, "")
			continue
		}

		flushList()
		out = append(out, applyTeamsInline(html.EscapeString(line)))
	}

	if inFence {
		// Unclosed fence — emit what we have.
		flushFence()
	}
	flushList()

	return strings.Join(out, "\n")
}

// applyTeamsInline applies inline markdown transforms (bold, italic, code,
// links, strikethrough) on already-HTML-escaped text. Bold is processed
// before italic so `**...**` doesn't get caught by the single-asterisk rule.
func applyTeamsInline(line string) string {
	// Inline code first — protects its contents from other transforms.
	line = inlineCodeRe.ReplaceAllString(line, "<code>$1</code>")
	line = boldRe.ReplaceAllString(line, "<strong>$1</strong>")
	line = strikethroughRe.ReplaceAllString(line, "<s>$1</s>")
	line = italicRe.ReplaceAllString(line, "<em>$1</em>")
	line = linkRe.ReplaceAllString(line, `<a href="$2">$1</a>`)
	return line
}

// TeamsHTMLToPlain extracts plain text from a Teams chatMessage HTML body.
//
// Strategy: replace block-level tags with newlines, replace <at> tags with
// their visible text, strip all remaining tags, decode entities, collapse
// runs of whitespace. The result is suitable for sending to the LLM as the
// user's prompt.
func TeamsHTMLToPlain(s string) string {
	// Preserve @mention display text.
	s = atTagRe.ReplaceAllString(s, "@$1")

	// Block-level boundaries become newlines.
	s = blockOpenRe.ReplaceAllString(s, "\n")
	s = blockCloseRe.ReplaceAllString(s, "\n")
	s = brRe.ReplaceAllString(s, "\n")

	// Strip remaining tags.
	s = anyTagRe.ReplaceAllString(s, "")

	// Decode entities.
	s = html.UnescapeString(s)

	// Collapse internal runs of whitespace to a single space; preserve newlines.
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(wsRunRe.ReplaceAllString(line, " "))
		if trimmed == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(trimmed)
	}
	return b.String()
}

// SplitMessageTeams splits text into chunks that each fit within the Teams
// body limit. Prefers paragraph boundaries, then newlines, then hard splits.
// Mirrors the existing SplitMessage helper but uses the Teams threshold.
func SplitMessageTeams(text string) []string {
	return SplitMessage(text, teamsBodyLimit)
}

// ExtractMention reports whether userID is @-mentioned in the message body.
// It checks the parsed mentions[] array first (authoritative), then falls
// back to scanning the body for <at id="N"> tags whose corresponding
// mentions entry resolves to userID.
func ExtractMention(body string, mentions []TeamsMention, userID string) bool {
	if userID == "" {
		return false
	}
	for _, m := range mentions {
		if m.Mentioned.User.ID == userID {
			return true
		}
	}
	// Belt-and-braces: an <at id="N"> tag whose N corresponds to a mention
	// entry for our user. This catches edge cases where the body contains
	// the tag but the mentions[] array was elided or malformed.
	for _, m := range mentions {
		if m.Mentioned.User.ID != userID {
			continue
		}
		needle := `<at id="` + itoa(m.ID) + `"`
		if strings.Contains(body, needle) {
			return true
		}
	}
	return false
}

// Regexes specific to the Teams HTML subset. The markdown package already
// defines headerRe / blockquoteRe / bulletRe / boldRe / italicRe /
// inlineCodeRe / linkRe / strikethroughRe — reuse those.
var (
	orderedListRe = regexp.MustCompile(`^\s*\d+\.\s+(.+)$`)

	// Inbound HTML stripping.
	atTagRe      = regexp.MustCompile(`(?s)<at[^>]*>([^<]*)</at>`)
	blockOpenRe  = regexp.MustCompile(`(?i)<(p|div|li|h[1-6]|blockquote|pre)[^>]*>`)
	blockCloseRe = regexp.MustCompile(`(?i)</(p|div|li|h[1-6]|blockquote|pre|ul|ol)>`)
	brRe         = regexp.MustCompile(`(?i)<br\s*/?>`)
	anyTagRe     = regexp.MustCompile(`<[^>]+>`)
	wsRunRe      = regexp.MustCompile(`[ \t]+`)
)

// itoa is a fast int→string for small positive ints. Avoids strconv import
// noise in this file; the markdown package keeps its zero-dep footprint.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
