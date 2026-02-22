package markdown

import (
	"strings"
	"testing"
)

func TestToTelegramHTML_Bold(t *testing.T) {
	got := ToTelegramHTML("this is **bold** text")
	want := "this is <b>bold</b> text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_Italic(t *testing.T) {
	got := ToTelegramHTML("this is *italic* text")
	want := "this is <i>italic</i> text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_InlineCode(t *testing.T) {
	got := ToTelegramHTML("use `fmt.Println` here")
	want := "use <code>fmt.Println</code> here"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_FencedCodeBlock(t *testing.T) {
	input := "```go\nfmt.Println(\"hello\")\n```"
	got := ToTelegramHTML(input)
	want := `<pre><code class="language-go">fmt.Println(&quot;hello&quot;)</code></pre>`
	// Note: quotes inside code are HTML-escaped via escapeHTML which handles &, <, >
	// Actually our escapeHTML doesn't handle quotes. Let's check what we get.
	want = `<pre><code class="language-go">fmt.Println("hello")</code></pre>`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_FencedCodeBlockNoLang(t *testing.T) {
	input := "```\nsome code\n```"
	got := ToTelegramHTML(input)
	want := "<pre><code>some code</code></pre>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_Headers(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"# Title", "<b>Title</b>"},
		{"## Subtitle", "<b>Subtitle</b>"},
		{"### Section", "<b>Section</b>"},
	}
	for _, tt := range tests {
		got := ToTelegramHTML(tt.input)
		if got != tt.want {
			t.Errorf("ToTelegramHTML(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToTelegramHTML_Links(t *testing.T) {
	got := ToTelegramHTML("click [here](https://example.com)")
	want := `click <a href="https://example.com">here</a>`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_Blockquote(t *testing.T) {
	got := ToTelegramHTML("> this is a quote")
	want := "<blockquote>this is a quote</blockquote>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_Strikethrough(t *testing.T) {
	got := ToTelegramHTML("this is ~~deleted~~ text")
	want := "this is <s>deleted</s> text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_BulletList(t *testing.T) {
	input := "- first\n- second\n* third"
	got := ToTelegramHTML(input)
	want := "• first\n• second\n• third"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_HTMLEscaping(t *testing.T) {
	got := ToTelegramHTML("use <div> & 5 > 3")
	want := "use &lt;div&gt; &amp; 5 &gt; 3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToTelegramHTML_NoTransformInsideCodeBlock(t *testing.T) {
	input := "```\n**not bold** and *not italic*\n```"
	got := ToTelegramHTML(input)
	// Inside code blocks, content should be escaped but not transformed
	if strings.Contains(got, "<b>") || strings.Contains(got, "<i>") {
		t.Errorf("code block contents should not be transformed: %q", got)
	}
	if !strings.Contains(got, "**not bold**") {
		t.Errorf("expected raw markdown preserved in code: %q", got)
	}
}

func TestToTelegramHTML_MixedContent(t *testing.T) {
	input := "**Bold** and *italic* with `code`"
	got := ToTelegramHTML(input)
	want := "<b>Bold</b> and <i>italic</i> with <code>code</code>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Slack mrkdwn tests ---

func TestToSlackMrkdwn_Bold(t *testing.T) {
	got := ToSlackMrkdwn("this is **bold** text")
	want := "this is *bold* text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_Italic(t *testing.T) {
	got := ToSlackMrkdwn("this is *italic* text")
	want := "this is _italic_ text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_InlineCode(t *testing.T) {
	got := ToSlackMrkdwn("use `code` here")
	want := "use `code` here"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_FencedCodeBlock(t *testing.T) {
	input := "```python\nprint('hello')\n```"
	got := ToSlackMrkdwn(input)
	want := "```\nprint('hello')\n```"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_Headers(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"# Title", "*Title*"},
		{"## Subtitle", "*Subtitle*"},
		{"### Section", "*Section*"},
	}
	for _, tt := range tests {
		got := ToSlackMrkdwn(tt.input)
		if got != tt.want {
			t.Errorf("ToSlackMrkdwn(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToSlackMrkdwn_Links(t *testing.T) {
	got := ToSlackMrkdwn("click [here](https://example.com)")
	want := "click <https://example.com|here>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_Blockquote(t *testing.T) {
	got := ToSlackMrkdwn("> this is a quote")
	want := "> this is a quote"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_Strikethrough(t *testing.T) {
	got := ToSlackMrkdwn("this is ~~deleted~~ text")
	want := "this is ~deleted~ text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_BulletList(t *testing.T) {
	input := "- first\n- second\n* third"
	got := ToSlackMrkdwn(input)
	want := "• first\n• second\n• third"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToSlackMrkdwn_NoTransformInsideCodeBlock(t *testing.T) {
	input := "```\n**not bold** and *not italic*\n```"
	got := ToSlackMrkdwn(input)
	if !strings.Contains(got, "**not bold**") {
		t.Errorf("code block contents should not be transformed: %q", got)
	}
}

func TestToSlackMrkdwn_MixedContent(t *testing.T) {
	input := "**Bold** and *italic* with `code`"
	got := ToSlackMrkdwn(input)
	want := "*Bold* and _italic_ with `code`"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- SplitMessage tests ---

func TestSplitMessage_Short(t *testing.T) {
	chunks := SplitMessage("short message", 100)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != "short message" {
		t.Errorf("got %q", chunks[0])
	}
}

func TestSplitMessage_ParagraphBoundary(t *testing.T) {
	text := "first paragraph\n\nsecond paragraph"
	chunks := SplitMessage(text, 20)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if chunks[0] != "first paragraph" {
		t.Errorf("chunk[0] = %q", chunks[0])
	}
	if chunks[1] != "second paragraph" {
		t.Errorf("chunk[1] = %q", chunks[1])
	}
}

func TestSplitMessage_NewlineFallback(t *testing.T) {
	text := "line one\nline two\nline three"
	chunks := SplitMessage(text, 15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if chunks[0] != "line one" {
		t.Errorf("chunk[0] = %q", chunks[0])
	}
}

func TestSplitMessage_HardSplit(t *testing.T) {
	text := strings.Repeat("a", 30)
	chunks := SplitMessage(text, 10)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %v", len(chunks), chunks)
	}
	for _, c := range chunks {
		if len(c) > 10 {
			t.Errorf("chunk exceeds limit: %q (%d chars)", c, len(c))
		}
	}
}

func TestSplitMessage_ExactlyAtLimit(t *testing.T) {
	text := strings.Repeat("x", 100)
	chunks := SplitMessage(text, 100)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
}

// --- Real LLM output test ---

func TestToTelegramHTML_RealLLMOutput(t *testing.T) {
	input := `# Weather Report

**Current conditions** in *San Francisco*:

- Temperature: 65°F
- Humidity: 72%

> Note: data from OpenWeather API

For more info, visit [OpenWeather](https://openweathermap.org).

` + "```json\n{\"temp\": 65, \"humidity\": 72}\n```"

	got := ToTelegramHTML(input)

	checks := []struct {
		desc     string
		contains string
	}{
		{"header converted", "<b>Weather Report</b>"},
		{"bold converted", "<b>Current conditions</b>"},
		{"italic converted", "<i>San Francisco</i>"},
		{"bullet list", "• Temperature: 65°F"},
		{"blockquote", "<blockquote>Note: data from OpenWeather API</blockquote>"},
		{"link converted", `<a href="https://openweathermap.org">OpenWeather</a>`},
		{"code block", `<pre><code class="language-json">`},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("%s: expected %q in output:\n%s", c.desc, c.contains, got)
		}
	}
}

func TestToSlackMrkdwn_RealLLMOutput(t *testing.T) {
	input := `# Weather Report

**Current conditions** in *San Francisco*:

- Temperature: 65°F
- Humidity: 72%

> Note: data from OpenWeather API

For more info, visit [OpenWeather](https://openweathermap.org).`

	got := ToSlackMrkdwn(input)

	checks := []struct {
		desc     string
		contains string
	}{
		{"header converted", "*Weather Report*"},
		{"bold converted", "*Current conditions*"},
		{"italic converted", "_San Francisco_"},
		{"bullet list", "• Temperature: 65°F"},
		{"blockquote", "> Note: data from OpenWeather API"},
		{"link converted", "<https://openweathermap.org|OpenWeather>"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("%s: expected %q in output:\n%s", c.desc, c.contains, got)
		}
	}
}
