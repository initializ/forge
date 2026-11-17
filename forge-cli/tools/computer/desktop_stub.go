//go:build !darwin || !cgo

package computer

import (
	"context"
	"fmt"
	"runtime"
)

// DesktopBackend stub for any build without the native desktop backend — a
// non-macOS OS, or a macOS build with cgo disabled (robotgo needs cgo + Xcode).
// The runner only wires the desktop profile when DesktopSupported() is true, so
// this is a safety net rather than a reachable path.
type DesktopBackend struct{}

// NewDesktopBackend returns the (unsupported) desktop backend for this build.
func NewDesktopBackend() *DesktopBackend { return &DesktopBackend{} }

// DesktopSupported reports whether the native desktop backend is available in
// this build (macOS + cgo). False here.
func DesktopSupported() bool { return false }

func (b *DesktopBackend) Name() string { return "desktop-unsupported" }

func (b *DesktopBackend) Screenshot(context.Context) ([]byte, int, int, error) {
	return nil, 0, 0, fmt.Errorf("desktop computer use requires a macOS build with cgo (GOOS=%s, cgo may be disabled)", runtime.GOOS)
}
