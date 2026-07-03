package browser

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// binaryCandidates returns chromium binary names/paths to probe, in preference
// order. PATH lookups come first; platform-specific absolute paths follow.
func binaryCandidates() []string {
	candidates := []string{
		"chromium",
		"chromium-browser",
		"google-chrome",
		"google-chrome-stable",
		"chrome",
		"headless-shell",
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	return candidates
}

// ResolveBinary locates a Chromium-compatible browser binary. FORGE_BROWSER_BIN
// takes precedence and must point at an existing file; otherwise candidates are
// probed via exec.LookPath (absolute candidates via os.Stat).
func ResolveBinary() (string, error) {
	if override := os.Getenv("FORGE_BROWSER_BIN"); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("FORGE_BROWSER_BIN=%q: %w", override, err)
		}
		return override, nil
	}
	for _, c := range binaryCandidates() {
		if len(c) > 0 && c[0] == '/' {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no chromium-compatible browser found (tried %v); install chromium or set FORGE_BROWSER_BIN", binaryCandidates())
}

// HeadlessFromEnv reports whether the browser should run headless.
// Defaults to true; FORGE_BROWSER_HEADLESS=false or 0 opts into headful mode
// for local debugging.
func HeadlessFromEnv() bool {
	switch os.Getenv("FORGE_BROWSER_HEADLESS") {
	case "false", "0":
		return false
	}
	return true
}
