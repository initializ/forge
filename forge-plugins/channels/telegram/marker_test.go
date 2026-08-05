package telegram

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
)

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
