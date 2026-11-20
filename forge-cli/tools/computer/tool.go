package computer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/initializ/forge/forge-core/llm"
	coretools "github.com/initializ/forge/forge-core/tools"
)

// screenshotTool captures the screen and returns it to the MODEL to see, via the
// image-bearing-tool-result capability (#584/#582). It implements
// coretools.MediaResultTool, so the executor projects the screenshot into the
// tool-result message as an image the vision model can reason about — the
// read-only foundation the Phase-2 control actions build on.
type screenshotTool struct {
	backend Backend
}

func (t *screenshotTool) Name() string { return "computer_screenshot" }

func (t *screenshotTool) Description() string {
	return "Capture a screenshot of the computer screen and return it so you can SEE the current " +
		"state (windows, dialogs, cursor). Use it to observe before deciding what to do next. " +
		"Read-only: it does not click or type."
}

func (t *screenshotTool) Category() coretools.Category { return coretools.CategoryBuiltin }

func (t *screenshotTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Execute is the text-only fallback (surfaces that can't show the model an
// image): it still captures, but reports that the image isn't visible here.
func (t *screenshotTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	_, w, h, err := t.backend.Screenshot(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Screenshot captured (%dx%d), but this surface cannot display it to the model.", w, h), nil
}

// ExecuteWithMedia captures the screen and returns the PNG as media the model
// sees (#584). The bytes ride inline for the turn only (not persisted).
func (t *screenshotTool) ExecuteWithMedia(ctx context.Context, _ json.RawMessage) (string, []llm.MediaRef, error) {
	png, w, h, err := t.backend.Screenshot(ctx)
	if err != nil {
		return "", nil, err
	}
	// Downscale a Retina/large capture so it stays under the provider's per-image
	// size ceiling (and keeps frames cheap); small captures pass through (#585).
	png, w, h, err = normalizeScreenshot(png, w, h)
	if err != nil {
		return "", nil, err
	}
	text := fmt.Sprintf("Screenshot captured (%dx%d) via %s.", w, h, t.backend.Name())
	return text, []llm.MediaRef{{MimeType: "image/png", Bytes: png}}, nil
}

// RegisterTools registers the read-only computer-use tool family against the
// given backend (#582 Phase 1).
func RegisterTools(reg *coretools.Registry, backend Backend) error {
	if backend == nil {
		return fmt.Errorf("computer: RegisterTools requires a backend")
	}
	return reg.Register(&screenshotTool{backend: backend})
}
