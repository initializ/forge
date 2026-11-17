//go:build !darwin

package computer

import (
	"context"
	"fmt"
	"runtime"
)

// DesktopBackend on non-macOS is a stub: the read-only screencapture path is
// macOS-only in Phase 1. Linux desktop CU is intentionally out of scope (it is
// the containerized browser profile), and Windows gets its own backend in a
// later phase. The runner only wires DesktopBackend on darwin, so this is a
// safety net rather than a reachable path.
type DesktopBackend struct{}

// NewDesktopBackend returns the (unsupported) desktop backend on this OS.
func NewDesktopBackend() *DesktopBackend { return &DesktopBackend{} }

// DesktopSupported reports whether read-only desktop capture is available on
// this OS (macOS only in Phase 1).
func DesktopSupported() bool { return false }

func (b *DesktopBackend) Name() string { return "desktop-unsupported" }

func (b *DesktopBackend) Screenshot(context.Context) ([]byte, int, int, error) {
	return nil, 0, 0, fmt.Errorf("desktop computer use is not supported on %s (Phase 1 is macOS-only)", runtime.GOOS)
}
