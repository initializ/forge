package browser

import (
	"errors"
	"fmt"

	coretools "github.com/initializ/forge/forge-core/tools"
)

// ToolNames lists the browser tool family in registration order. Useful for
// tests and for denied_tools documentation.
var ToolNames = []string{
	"browser_navigate",
	"browser_state",
	"browser_click",
	"browser_fill",
	"browser_extract",
	"browser_screenshot",
}

// RegisterTools registers the browser tool family against a live Manager.
// Fail-closed: a manager without a proxy must never have been constructed
// (NewManager enforces it), but re-assert here since this is the last gate
// before the LLM can drive the browser.
func RegisterTools(reg *coretools.Registry, m *Manager) error {
	if m == nil {
		return errors.New("browser: RegisterTools requires a manager")
	}
	if m.cfg.ProxyURL == "" {
		return errors.New("browser: refusing to register tools without an egress proxy")
	}
	for _, t := range []coretools.Tool{
		&navigateTool{m: m},
		&stateTool{m: m},
		&clickTool{m: m},
		&fillTool{m: m},
		&extractTool{m: m},
		&screenshotTool{m: m},
	} {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("browser: register %s: %w", t.Name(), err)
		}
	}
	return nil
}
