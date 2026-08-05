package telegram

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
)

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
			"bare marker no note",
			"data<<ctxzip:0123456789ab>>",
			"data",
		},
		{"malformed left intact", "text <<ctxzip:not-hex blah", "text <<ctxzip:not-hex blah"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripCompressionMarkers(tt.in); got != tt.want {
				t.Errorf("stripCompressionMarkers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestExtractText_StripsCompressionMarker pins that a leaked ctxzip marker
// never reaches a Telegram chat through the text path.
func TestExtractText_StripsCompressionMarker(t *testing.T) {
	msg := &a2a.Message{Parts: []a2a.Part{
		a2a.NewTextPart("Summary of results. <<ctxzip:2cda42236849 55_lines_offloaded>>"),
	}}
	if got := extractText(msg); strings.Contains(got, "ctxzip") {
		t.Errorf("extractText leaked a compression marker: %q", got)
	}
}

// TestExtractLargestFile_StripsCompressionMarker: a compressed markdown
// deliverable carries no marker into the uploaded document either.
func TestExtractLargestFile_StripsCompressionMarker(t *testing.T) {
	msg := &a2a.Message{Parts: []a2a.Part{
		a2a.NewFilePart(a2a.FileContent{
			Name:  "report.md",
			Bytes: []byte("# Report\n\nfindings <<ctxzip:abcabcabcabc 9_rows_offloaded>>"),
		}),
	}}
	content, name := extractLargestFile(msg)
	if name != "report.md" {
		t.Fatalf("filename = %q, want report.md", name)
	}
	if strings.Contains(content, "ctxzip") {
		t.Errorf("extractLargestFile leaked a compression marker: %q", content)
	}
}
