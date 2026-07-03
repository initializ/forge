package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
)

func TestFileArtifactFromToolResult_FileCreate(t *testing.T) {
	result, _ := json.Marshal(map[string]any{
		"filename":  "report.md",
		"content":   "# Report\nDone.",
		"mime_type": "text/markdown",
		"path":      "/tmp/whatever/report.md",
	})
	part := fileArtifactFromToolResult("file_create", string(result))
	if part == nil {
		t.Fatal("file_create result produced no artifact")
	}
	if part.Kind != a2a.PartKindFile || part.File == nil {
		t.Fatalf("part = %+v, want file part", part)
	}
	if part.File.Name != "report.md" || string(part.File.Bytes) != "# Report\nDone." {
		t.Errorf("file part = %q %q", part.File.Name, part.File.Bytes)
	}
}

func TestFileArtifactFromToolResult_BrowserScreenshotPath(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "shot.png")
	pngBytes := []byte("\x89PNG\r\n\x1a\nfakepixels")
	if err := os.WriteFile(pngPath, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]any{
		"filename":   "shot.png",
		"mime_type":  "image/png",
		"path":       pngPath,
		"size_bytes": len(pngBytes),
	})
	part := fileArtifactFromToolResult("browser_screenshot", string(result))
	if part == nil {
		t.Fatal("browser_screenshot result produced no artifact")
	}
	if part.File.MimeType != "image/png" || string(part.File.Bytes) != string(pngBytes) {
		t.Errorf("screenshot artifact = %q (%d bytes), want PNG bytes from path", part.File.MimeType, len(part.File.Bytes))
	}
}

func TestFileArtifactFromToolResult_Negative(t *testing.T) {
	cases := []struct {
		name, tool, result string
	}{
		{"other tool", "http_request", `{"filename":"x.png","path":"/nope"}`},
		{"not json", "browser_screenshot", "navigate ok"},
		{"missing filename", "browser_screenshot", `{"path":"/nope"}`},
		{"unreadable path", "browser_screenshot", `{"filename":"x.png","path":"/nonexistent/x.png"}`},
		{"empty everything", "file_create", `{"filename":"x.md"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if part := fileArtifactFromToolResult(tc.tool, tc.result); part != nil {
				t.Errorf("got artifact %+v, want nil", part)
			}
		})
	}
}

func TestIsIntermediateOutputTool(t *testing.T) {
	for tool, want := range map[string]bool{
		"cli_execute":     true,
		"browser_extract": true,
		"browser_state":   true,
		"http_request":    false,
		"web_search":      false,
	} {
		if got := isIntermediateOutputTool(tool); got != want {
			t.Errorf("isIntermediateOutputTool(%q) = %v, want %v", tool, got, want)
		}
	}
	// A 20k browser_extract must NOT be auto-attached (it's an intermediate
	// observation), while an unknown tool of the same size still is. This
	// mirrors the branch in the executor loop.
	big := strings.Repeat("x", 20000)
	if !(len(big) > 8000) || isIntermediateOutputTool("browser_extract") == false {
		t.Fatal("test premise broken")
	}
}
