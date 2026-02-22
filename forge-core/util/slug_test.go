package util

import "testing"

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"My Cool Agent", "my-cool-agent"},
		{"hello world", "hello-world"},
		{"  Leading Spaces  ", "leading-spaces"},
		{"UPPER CASE", "upper-case"},
		{"special!@#$%chars", "specialchars"},
		{"multiple---hyphens", "multiple-hyphens"},
		{"--leading-trailing--", "leading-trailing"},
		{"123-numbers-456", "123-numbers-456"},
		{"", ""},
		{"---", ""},
		{"a", "a"},
		{"Hello   World", "hello---world"},  // spaces become hyphens, then collapsed
		{"café-agent", "caf-agent"},          // non-ascii stripped
		{"my_agent_name", "myagentname"},     // underscores stripped
		{"  --hello--world--  ", "hello-world"},
	}

	// Fix expected: "Hello   World" → three spaces → three hyphens → collapsed to one
	tests[11].want = "hello-world"

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
