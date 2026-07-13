package runtime

import (
	"context"
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
	part := fileArtifactFromToolResult(context.Background(), "file_create", string(result))
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

// TestFileArtifactFromToolResult_FileCreateEmptyContent pins the historical
// behavior: an empty-content file_create still attaches (a zero-byte part).
func TestFileArtifactFromToolResult_FileCreateEmptyContent(t *testing.T) {
	part := fileArtifactFromToolResult(context.Background(), "file_create", `{"filename":"empty.txt","content":""}`)
	if part == nil || part.File == nil || part.File.Name != "empty.txt" {
		t.Fatalf("empty file_create = %+v, want a zero-byte artifact", part)
	}
	if len(part.File.Bytes) != 0 {
		t.Errorf("bytes = %d, want 0", len(part.File.Bytes))
	}
}

func TestFileArtifactFromToolResult_BrowserScreenshotConfined(t *testing.T) {
	filesDir := t.TempDir()
	pngPath := filepath.Join(filesDir, "shot.png")
	pngBytes := []byte("\x89PNG\r\n\x1a\nfakepixels")
	if err := os.WriteFile(pngPath, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithFilesDir(context.Background(), filesDir)
	result, _ := json.Marshal(map[string]any{
		"filename":   "shot.png",
		"mime_type":  "image/png",
		"path":       pngPath,
		"size_bytes": len(pngBytes),
	})
	part := fileArtifactFromToolResult(ctx, "browser_screenshot", string(result))
	if part == nil {
		t.Fatal("in-tree browser_screenshot produced no artifact")
	}
	if part.File.MimeType != "image/png" || string(part.File.Bytes) != string(pngBytes) {
		t.Errorf("screenshot artifact = %q (%d bytes), want PNG bytes from path", part.File.MimeType, len(part.File.Bytes))
	}
}

// TestFileArtifactFromToolResult_ScreenshotPathConfinement is the security
// regression: a browser_screenshot-named result pointing outside the agent
// files directory must NOT be read (no read-any-file→channel exfiltration).
func TestFileArtifactFromToolResult_ScreenshotPathConfinement(t *testing.T) {
	filesDir := t.TempDir()
	ctx := WithFilesDir(context.Background(), filesDir)

	// A real secret outside the files dir.
	outside := filepath.Join(t.TempDir(), "secrets.enc")
	if err := os.WriteFile(outside, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"absolute outside files dir": outside,
		"parent traversal":           filepath.Join(filesDir, "..", filepath.Base(outside)),
		"etc passwd":                 "/etc/passwd",
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			result, _ := json.Marshal(map[string]any{"filename": "x.png", "path": p, "mime_type": "image/png"})
			if part := fileArtifactFromToolResult(ctx, "browser_screenshot", string(result)); part != nil {
				t.Errorf("out-of-tree path %q was read and attached (%d bytes) — exfiltration not prevented", p, len(part.File.Bytes))
			}
		})
	}

	// With no files dir on the context, path reads are rejected outright.
	result, _ := json.Marshal(map[string]any{"filename": "x.png", "path": outside, "mime_type": "image/png"})
	if part := fileArtifactFromToolResult(context.Background(), "browser_screenshot", string(result)); part != nil {
		t.Errorf("path read with no files dir was allowed: %+v", part)
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if part := fileArtifactFromToolResult(context.Background(), tc.tool, tc.result); part != nil {
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
		// A non-browser tool that merely starts with "browser_" must NOT be
		// treated as intermediate (explicit-set matching, not prefix).
		"browser_export_report": false,
	} {
		if got := isIntermediateOutputTool(tool); got != want {
			t.Errorf("isIntermediateOutputTool(%q) = %v, want %v", tool, got, want)
		}
	}
	big := strings.Repeat("x", 20000)
	if len(big) <= 8000 || !isIntermediateOutputTool("browser_extract") {
		t.Fatal("test premise broken")
	}
}
