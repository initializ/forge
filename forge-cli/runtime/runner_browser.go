package runtime

import (
	"fmt"

	"github.com/initializ/forge/forge-skills/contract"
)

// browserRegistrationDecision decides whether the browser tool family can be
// registered, given the derived capability config, the resolved Chromium
// binary (binPath/resErr from browser.ResolveBinary), and the egress proxy
// address. Pure function so the gate is table-testable without a browser.
//
// Fail-closed rules:
//   - no capability declared → no registration (and no reason: not an error)
//   - capability declared but no Chromium → skip with an actionable reason
//   - capability declared but no proxy → skip; the browser must never run
//     unproxied
func browserRegistrationDecision(derived *contract.DerivedBrowserConfig, binPath string, resErr error, proxyURL string) (bool, string) {
	if derived == nil {
		return false, ""
	}
	if resErr != nil || binPath == "" {
		return false, fmt.Sprintf("no chromium-compatible browser found (%v); install chromium in the agent image or set FORGE_BROWSER_BIN", resErr)
	}
	if proxyURL == "" {
		return false, "egress proxy unavailable; refusing to run an unproxied browser"
	}
	return true, ""
}
