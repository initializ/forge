package markdown

import "testing"

func TestStripCompressionMarkers(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no marker", "plain text", "plain text"},
		{
			"trailing marker",
			"Here are the issues.\n\n<<ctxzip:2cda42236849 55_lines_offloaded>>",
			"Here are the issues.",
		},
		{
			"inline marker",
			"before <<ctxzip:abc123abc123 12_rows_offloaded>> after",
			"before  after",
		},
		{
			"multiple markers",
			"<<ctxzip:aaaaaaaaaaaa 1_rows_offloaded>>x<<ctxzip:bbbbbbbbbbbb 2_rows_offloaded>>",
			"x",
		},
		{
			"bare marker no note",
			"data<<ctxzip:0123456789ab>>",
			"data",
		},
		// A "<<ctxzip:" that is not a well-formed marker is left alone rather
		// than eating the rest of the string.
		{"malformed left intact", "text <<ctxzip:not-hex blah", "text <<ctxzip:not-hex blah"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripCompressionMarkers(tt.in); got != tt.want {
				t.Errorf("StripCompressionMarkers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
