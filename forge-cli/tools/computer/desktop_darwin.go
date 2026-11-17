//go:build darwin && cgo

package computer

import (
	"bytes"
	"context"
	"fmt"
	"image/png"

	"github.com/go-vgo/robotgo"
)

// DesktopBackend captures (and, in Phase 2, will control) the macOS host screen
// via robotgo — the single cross-platform native backend decided for Profile B
// (#582 decision 1). It uses CoreGraphics under the hood, so screen capture
// requires the Screen Recording TCC permission; without it macOS returns a
// black/empty image. Phase 1 exposes only Screenshot; robotgo's mouse/keyboard
// APIs back the Phase-2 control actions on this same backend.
//
// Built only with `darwin && cgo` (robotgo is cgo + requires Xcode frameworks);
// every other build uses the stub in desktop_stub.go.
type DesktopBackend struct{}

// NewDesktopBackend returns the macOS robotgo backend.
func NewDesktopBackend() *DesktopBackend { return &DesktopBackend{} }

// DesktopSupported reports whether the native desktop backend is available in
// this build (macOS + cgo).
func DesktopSupported() bool { return true }

func (b *DesktopBackend) Name() string { return "desktop-macos" }

func (b *DesktopBackend) Screenshot(ctx context.Context) ([]byte, int, int, error) {
	// CaptureScreen is a blocking cgo call with no ctx hook; honor cancellation
	// up front so a cancelled turn doesn't start a capture.
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}

	bit := robotgo.CaptureScreen() // main display, full size
	if bit == nil {
		return nil, 0, 0, fmt.Errorf("screen capture failed (grant Screen Recording permission?)")
	}
	defer robotgo.FreeBitmap(bit)

	img := robotgo.ToImage(bit)
	if img == nil {
		return nil, 0, 0, fmt.Errorf("screen capture produced no image (Screen Recording permission may be denied)")
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, fmt.Errorf("encoding screenshot: %w", err)
	}
	r := img.Bounds()
	return buf.Bytes(), r.Dx(), r.Dy(), nil
}
