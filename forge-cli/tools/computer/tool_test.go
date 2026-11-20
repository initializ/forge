package computer

import (
	"context"
	"errors"
	"testing"

	coretools "github.com/initializ/forge/forge-core/tools"
)

// fakeBackend returns canned screenshot bytes (no real screen capture).
type fakeBackend struct {
	png  []byte
	w, h int
	err  error
	name string
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) Screenshot(context.Context) ([]byte, int, int, error) {
	return f.png, f.w, f.h, f.err
}

// Compile-time proof the tool implements the #584 image-bearing-tool-result
// capability, so the executor sends the screenshot to the model.
var _ coretools.MediaResultTool = (*screenshotTool)(nil)

func TestScreenshotTool_ReturnsImageMedia(t *testing.T) {
	// Within the downscale bound so the (non-PNG) fake bytes pass through
	// unchanged — this test asserts the media plumbing, not the resize.
	be := &fakeBackend{png: []byte{0x89, 'P', 'N', 'G', 1, 2}, w: 800, h: 600, name: "desktop-test"}
	tool := &screenshotTool{backend: be}

	text, media, err := tool.ExecuteWithMedia(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(media) != 1 || media[0].MimeType != "image/png" || len(media[0].Bytes) == 0 {
		t.Fatalf("expected one image/png media, got %+v", media)
	}
	if text == "" {
		t.Error("expected a text confirmation alongside the image")
	}

	// Text-only fallback still captures but reports the image isn't shown.
	ftext, err := tool.Execute(context.Background(), nil)
	if err != nil || ftext == "" {
		t.Errorf("Execute fallback: text=%q err=%v", ftext, err)
	}
}

func TestScreenshotTool_PropagatesBackendError(t *testing.T) {
	tool := &screenshotTool{backend: &fakeBackend{err: errors.New("permission denied")}}
	if _, _, err := tool.ExecuteWithMedia(context.Background(), nil); err == nil {
		t.Error("backend error must propagate")
	}
}

func TestRegisterTools(t *testing.T) {
	reg := coretools.NewRegistry()
	if err := RegisterTools(reg, &fakeBackend{name: "x"}); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	if reg.Get("computer_screenshot") == nil {
		t.Error("computer_screenshot not registered")
	}
	// Nil backend is rejected.
	if err := RegisterTools(coretools.NewRegistry(), nil); err == nil {
		t.Error("nil backend should error")
	}
}
