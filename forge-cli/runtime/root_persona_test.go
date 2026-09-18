package runtime

import "testing"

func TestStripYAMLFrontmatter(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "strips frontmatter block",
			in:   "---\nname: agent\ndescription: does things\n---\n\nYou are agent.\nBe helpful.",
			want: "\nYou are agent.\nBe helpful.",
		},
		{
			name: "no frontmatter returned unchanged",
			in:   "You are agent.",
			want: "You are agent.",
		},
		{
			name: "unterminated frontmatter returned unchanged",
			in:   "---\nname: agent\nno closing delimiter here",
			want: "---\nname: agent\nno closing delimiter here",
		},
		{
			name: "leading BOM before frontmatter",
			in:   "\ufeff---\nname: a\n---\nBody",
			want: "Body",
		},
		{
			name: "crlf line endings",
			in:   "---\r\nname: agent\r\n---\r\nBody here",
			want: "Body here",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripYAMLFrontmatter(c.in); got != c.want {
				t.Errorf("stripYAMLFrontmatter(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
