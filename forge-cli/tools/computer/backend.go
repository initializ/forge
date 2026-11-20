// Package computer implements the computer-use tool family (#582): a
// vision-driven screenshot→action loop. Phase 1 is read-only — it captures the
// screen so a vision model can see it, using the image-bearing-tool-result
// capability (#584). Control actions (click/type/key/scroll) land in Phase 2.
package computer

import "context"

// Backend abstracts the surface the computer tool operates on, so the same tool
// works against the host desktop (DesktopBackend, robotgo on macOS — decision 1)
// or the agent's browser viewport (a future BrowserBackend over the CDP browser
// Manager). Phase 1 exposes only the read-only Screenshot; robotgo's
// mouse/keyboard APIs back the Phase-2 control actions on the same backend.
type Backend interface {
	// Screenshot captures the current screen/viewport as PNG bytes plus its
	// pixel dimensions.
	Screenshot(ctx context.Context) (png []byte, width, height int, err error)
	// Name identifies the backend for audit and error messages
	// (e.g. "desktop-macos").
	Name() string
}
