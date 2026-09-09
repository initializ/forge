package markdown

import (
	"strings"
	"testing"
)

func TestToWhatsAppText_Bold(t *testing.T) {
	got := ToWhatsAppText("**hello** world")
	want := "*hello* world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_Italic(t *testing.T) {
	got := ToWhatsAppText("be *brave*")
	want := "be _brave_"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The asterisk inversion is the whole risk in this converter: markdown bold
// becomes a single asterisk, which is exactly what the italic rule matches.
func TestToWhatsAppText_BoldNotReclaimedByItalic(t *testing.T) {
	got := ToWhatsAppText("**bold** and *italic*")
	want := "*bold* and _italic_"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_Strikethrough(t *testing.T) {
	got := ToWhatsAppText("~~gone~~")
	want := "~gone~"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_InlineCodeUnchanged(t *testing.T) {
	got := ToWhatsAppText("call `foo()` here")
	want := "call `foo()` here"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Markdown syntax inside a code span is literal and must survive untouched.
func TestToWhatsAppText_InlineCodeProtectsMarkers(t *testing.T) {
	got := ToWhatsAppText("use `a **b** c` verbatim")
	want := "use `a **b** c` verbatim"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_MultipleInlineCodeSpans(t *testing.T) {
	got := ToWhatsAppText("`one` then **bold** then `two`")
	want := "`one` then *bold* then `two`"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_FencedCodeVerbatim(t *testing.T) {
	in := "```go\nx := **notbold**\n```"
	got := ToWhatsAppText(in)
	if got != in {
		t.Errorf("fenced code must pass through verbatim: got %q, want %q", got, in)
	}
}

func TestToWhatsAppText_Heading(t *testing.T) {
	got := ToWhatsAppText("# Title")
	want := "*Title*"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_HeadingAllLevels(t *testing.T) {
	got := ToWhatsAppText("### Deep")
	want := "*Deep*"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_Bullets(t *testing.T) {
	got := ToWhatsAppText("- one\n* two")
	want := "• one\n• two"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_NestedBulletsKeepIndent(t *testing.T) {
	got := ToWhatsAppText("- top\n  - nested")
	want := "• top\n  • nested"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_OrderedListKeepsNumbering(t *testing.T) {
	got := ToWhatsAppText("1. first\n2. second")
	want := "1. first\n2. second"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_Blockquote(t *testing.T) {
	got := ToWhatsAppText("> quoted **text**")
	want := "> quoted *text*"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_Link(t *testing.T) {
	got := ToWhatsAppText("see [the docs](https://example.com/x)")
	want := "see the docs: https://example.com/x"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A label that merely repeats the URL adds nothing — emit the URL alone.
func TestToWhatsAppText_LinkLabelSameAsURL(t *testing.T) {
	got := ToWhatsAppText("[https://example.com](https://example.com)")
	want := "https://example.com"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_Image(t *testing.T) {
	got := ToWhatsAppText("![a chart](https://example.com/c.png)")
	want := "a chart: https://example.com/c.png"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Images must be flattened before links, or the "!" is orphaned.
func TestToWhatsAppText_ImageLeavesNoStrayBang(t *testing.T) {
	got := ToWhatsAppText("![alt](u)")
	if strings.Contains(got, "!") {
		t.Errorf("stray %q in image conversion: %q", "!", got)
	}
}

func TestToWhatsAppText_Table(t *testing.T) {
	in := "| Name | Count |\n|------|-------|\n| foo  | 3     |"
	got := ToWhatsAppText(in)
	want := "Name — Count\n\nfoo — 3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_TableCellsGetInlineTransforms(t *testing.T) {
	got := ToWhatsAppText("| **a** | b |")
	want := "*a* — b"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_HorizontalRule(t *testing.T) {
	got := ToWhatsAppText("---")
	want := "──────────"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_PlainTextUnchanged(t *testing.T) {
	got := ToWhatsAppText("nothing special here")
	want := "nothing special here"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToWhatsAppText_Empty(t *testing.T) {
	if got := ToWhatsAppText(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// No output path may leak raw markdown bold/link syntax at the reader.
func TestToWhatsAppText_NoRawMarkdownLeaks(t *testing.T) {
	in := "# H\n\n**b** _i_ [t](u)\n\n- x\n\n| a | b |\n|---|---|\n| 1 | 2 |"
	got := ToWhatsAppText(in)
	for _, bad := range []string{"**", "](", "|---|"} {
		if strings.Contains(got, bad) {
			t.Errorf("output leaks %q: %q", bad, got)
		}
	}
}

func TestSplitMessageWhatsApp_ShortStaysWhole(t *testing.T) {
	chunks := SplitMessageWhatsApp("short")
	if len(chunks) != 1 || chunks[0] != "short" {
		t.Errorf("got %v, want single chunk", chunks)
	}
}

func TestSplitMessageWhatsApp_SplitsAtLimit(t *testing.T) {
	long := strings.Repeat("a", whatsappBodyLimit+100)
	chunks := SplitMessageWhatsApp(long)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > whatsappBodyLimit {
			t.Errorf("chunk %d over limit: %d > %d", i, len(c), whatsappBodyLimit)
		}
	}
}

func TestStripWhatsAppMention_ByNumber(t *testing.T) {
	got := StripWhatsAppMention("@14155550100 what is the status?", "14155550100", "forge")
	want := "what is the status?"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripWhatsAppMention_ByDisplayName(t *testing.T) {
	got := StripWhatsAppMention("@Forge Bot: deploy please", "14155550100", "Forge Bot")
	want := "deploy please"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripWhatsAppMention_CaseInsensitive(t *testing.T) {
	got := StripWhatsAppMention("@forge bot ping", "Forge Bot")
	want := "ping"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A mention in the middle of a sentence is part of the prompt, not an
// invocation prefix — leave it alone.
func TestStripWhatsAppMention_OnlyLeading(t *testing.T) {
	in := "tell @forge to stop"
	if got := StripWhatsAppMention(in, "forge"); got != in {
		t.Errorf("got %q, want unchanged %q", got, in)
	}
}

func TestStripWhatsAppMention_NoMatch(t *testing.T) {
	in := "plain message"
	if got := StripWhatsAppMention(in, "forge"); got != in {
		t.Errorf("got %q, want unchanged %q", got, in)
	}
}

func TestStripWhatsAppMention_EmptyHandlesIgnored(t *testing.T) {
	in := "@forge hello"
	got := StripWhatsAppMention(in, "", "  ", "forge")
	want := "hello"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
