//go:build darwin

package computer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png" // register PNG decoder for DecodeConfig
	"os"
	"os/exec"
)

// DesktopBackend captures the macOS host screen via the built-in `screencapture`
// utility — no cgo, no third-party deps. Read-only (Phase 1). Requires the
// Screen Recording permission (TCC); without it macOS returns a blank/denied
// capture. Control actions (Phase 2) will need an input backend (robotgo /
// cliclick) and are not part of this read-only backend.
type DesktopBackend struct{}

// NewDesktopBackend returns the macOS desktop backend.
func NewDesktopBackend() *DesktopBackend { return &DesktopBackend{} }

// DesktopSupported reports whether read-only desktop capture is available on
// this OS (macOS only in Phase 1).
func DesktopSupported() bool { return true }

func (b *DesktopBackend) Name() string { return "desktop-macos" }

func (b *DesktopBackend) Screenshot(ctx context.Context) ([]byte, int, int, error) {
	tmp, err := os.CreateTemp("", "forge-cu-*.png")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("screenshot temp file: %w", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()

	// -x: silent (no capture sound). -t png: PNG. Whole main display.
	cmd := exec.CommandContext(ctx, "screencapture", "-x", "-t", "png", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, 0, 0, fmt.Errorf("screencapture failed (grant Screen Recording permission?): %w: %s", err, string(out))
	}

	png, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading screenshot: %w", err)
	}
	if len(png) == 0 {
		return nil, 0, 0, fmt.Errorf("screencapture produced an empty image (Screen Recording permission may be denied)")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil {
		// Still return the bytes; dimensions are best-effort metadata.
		return png, 0, 0, nil
	}
	return png, cfg.Width, cfg.Height, nil
}
