package markdown

import (
	"strings"
	"testing"
)

func TestMarkdownToTeamsHTML_Bold(t *testing.T) {
	got := MarkdownToTeamsHTML("**hello** world")
	want := "<strong>hello</strong> world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToTeamsHTML_Italic(t *testing.T) {
	got := MarkdownToTeamsHTML("be *brave*")
	if !strings.Contains(got, "<em>brave</em>") {
		t.Errorf("expected <em>brave</em>, got %q", got)
	}
}

func TestMarkdownToTeamsHTML_InlineCode(t *testing.T) {
	got := MarkdownToTeamsHTML("call `foo()` here")
	if !strings.Contains(got, "<code>foo()</code>") {
		t.Errorf("expected <code>foo()</code>, got %q", got)
	}
}

func TestMarkdownToTeamsHTML_FencedCode(t *testing.T) {
	got := MarkdownToTeamsHTML("```go\nfmt.Println(\"hi\")\n```")
	if !strings.Contains(got, `<pre><code class="language-go">`) {
		t.Errorf("expected pre+code with language-go, got %q", got)
	}
	if !strings.Contains(got, "fmt.Println(") {
		t.Errorf("expected escaped code body, got %q", got)
	}
}

func TestMarkdownToTeamsHTML_Heading(t *testing.T) {
	got := MarkdownToTeamsHTML("# Title")
	want := "<h1>Title</h1>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToTeamsHTML_UnorderedList(t *testing.T) {
	got := MarkdownToTeamsHTML("- a\n- b\n- c")
	if !strings.Contains(got, "<ul><li>a</li><li>b</li><li>c</li></ul>") {
		t.Errorf("ul not formed: %q", got)
	}
}

func TestMarkdownToTeamsHTML_OrderedList(t *testing.T) {
	got := MarkdownToTeamsHTML("1. one\n2. two")
	if !strings.Contains(got, "<ol><li>one</li><li>two</li></ol>") {
		t.Errorf("ol not formed: %q", got)
	}
}

func TestMarkdownToTeamsHTML_Link(t *testing.T) {
	got := MarkdownToTeamsHTML("see [docs](https://x.example/y)")
	if !strings.Contains(got, `<a href="https://x.example/y">docs</a>`) {
		t.Errorf("link not formed: %q", got)
	}
}

func TestMarkdownToTeamsHTML_Blockquote(t *testing.T) {
	got := MarkdownToTeamsHTML("> quoted")
	want := "<blockquote>quoted</blockquote>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarkdownToTeamsHTML_HTMLEscape(t *testing.T) {
	got := MarkdownToTeamsHTML("a <script> & 5 > 3")
	for _, frag := range []string{"&lt;script&gt;", "&amp;", "&gt;"} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in %q", frag, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("raw <script> survived escape: %q", got)
	}
}

func TestTeamsHTMLToPlain_StripsTags(t *testing.T) {
	in := `<p>Hello <strong>world</strong></p><div>line2</div>`
	got := TeamsHTMLToPlain(in)
	want := "Hello world\nline2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTeamsHTMLToPlain_PreservesMentionText(t *testing.T) {
	in := `<p><at id="0">Forge Bot</at> please help</p>`
	got := TeamsHTMLToPlain(in)
	want := "@Forge Bot please help"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTeamsHTMLToPlain_DecodesEntities(t *testing.T) {
	in := `<p>5 &gt; 3 &amp;&amp; true</p>`
	got := TeamsHTMLToPlain(in)
	want := "5 > 3 && true"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTeamsHTMLToPlain_HandlesBR(t *testing.T) {
	in := `<p>line1<br/>line2<br>line3</p>`
	got := TeamsHTMLToPlain(in)
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitMessageTeams_UnderLimit(t *testing.T) {
	chunks := SplitMessageTeams("hello")
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("want single chunk, got %v", chunks)
	}
}

func TestSplitMessageTeams_OverLimit(t *testing.T) {
	// 60K chars, no newlines — forces hard split at the 24K boundary.
	long := strings.Repeat("a", 60000)
	chunks := SplitMessageTeams(long)
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > teamsBodyLimit {
			t.Errorf("chunk exceeds limit: %d > %d", len(c), teamsBodyLimit)
		}
	}
}

func TestExtractMention_MentionsArray(t *testing.T) {
	mentions := []TeamsMention{
		{ID: 0, Text: "Forge Bot", Mentioned: struct {
			User struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"user"`
		}{User: struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}{ID: "user-123", DisplayName: "Forge Bot"}}},
	}
	if !ExtractMention(`<at id="0">Forge Bot</at> hi`, mentions, "user-123") {
		t.Error("expected user-123 to be mentioned")
	}
	if ExtractMention(`<at id="0">Forge Bot</at> hi`, mentions, "other-user") {
		t.Error("did not expect other-user to be mentioned")
	}
}

func TestExtractMention_NoUserID(t *testing.T) {
	if ExtractMention("anything", nil, "") {
		t.Error("empty userID should never match")
	}
}
