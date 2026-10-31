package forgeui

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// imageGenParityCases is the capability matrix both the Go helper
// (runtime.ModelSupportsImageGen) and the JS mirror (modelSupportsImageGen in
// app.js) must agree on. It is the shared source of truth the reviewer asked
// for (#551): a table both sides are checked against, so the hand-maintained
// JS copy can't silently drift from the Go rule.
var imageGenParityCases = []struct{ provider, model string }{
	{"openai-responses", "gpt-5"},
	{"openai-responses", ""},
	{"OpenAI-Responses", "gpt-4o"},
	{"gemini", "gemini-3-pro-image"},
	{"gemini", "gemini-3.1-flash-lite-image"},
	{"gemini", "gemini-2.5-flash-image"},
	{"Gemini", "GEMINI-3-PRO-IMAGE"},
	{"gemini", "gemini-3.8-flash"},
	{"gemini", "gemini-4-argon"},
	{"gemini", ""},
	{"openai", "gpt-5"},
	{"anthropic", "claude-opus-4-8"},
	{"bedrock", "claude-sonnet-5"},
	{"ollama", "llama3.1"},
	{"", "gemini-3-pro-image"},
}

// extractJSFunction returns the source of the named top-level `function name(`
// declaration from src, brace-matched to its closing `}`.
func extractJSFunction(src, name string) (string, bool) {
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		return "", false
	}
	depth := 0
	seenOpen := false
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
			seenOpen = true
		case '}':
			depth--
			if seenOpen && depth == 0 {
				return src[start : i+1], true
			}
		}
	}
	return "", false
}

// TestModelSupportsImageGen_JSMirrorParity pins the JS mirror to the Go rule.
// When `node` is present it executes the actual JS function from app.js against
// the shared matrix and compares to the Go helper; otherwise it falls back to a
// canary that asserts the function still encodes the two rule clauses, so drift
// is caught even where node is unavailable.
func TestModelSupportsImageGen_JSMirrorParity(t *testing.T) {
	raw, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	fn, ok := extractJSFunction(string(raw), "modelSupportsImageGen")
	if !ok {
		t.Fatal("modelSupportsImageGen not found in app.js — did the JS mirror move or get renamed?")
	}

	node, nodeErr := exec.LookPath("node")
	if nodeErr != nil {
		// Canary: no JS runtime here, so at least assert the rule clauses the
		// Go helper depends on are still present (provider openai-responses →
		// true; gemini → name contains "image").
		for _, needle := range []string{"openai-responses", "gemini", "includes('image')"} {
			if !strings.Contains(fn, needle) {
				t.Errorf("JS mirror missing expected rule clause %q (node unavailable, canary check)", needle)
			}
		}
		t.Skip("node not found; ran canary clause-check only")
		return
	}

	cases := make([][2]string, len(imageGenParityCases))
	for i, c := range imageGenParityCases {
		cases[i] = [2]string{c.provider, c.model}
	}
	casesJSON, _ := json.Marshal(cases)

	// Harness: define the real function, run the matrix, print one bool/line.
	harness := fn + "\nconst cases = " + string(casesJSON) + ";\n" +
		"console.log(cases.map(c => modelSupportsImageGen(c[0], c[1]) ? 'true' : 'false').join('\\n'));\n"

	out, err := exec.Command(node, "-e", harness).Output()
	if err != nil {
		t.Fatalf("running JS mirror via node: %v", err)
	}
	jsResults := strings.Fields(strings.TrimSpace(string(out)))
	if len(jsResults) != len(imageGenParityCases) {
		t.Fatalf("JS returned %d results, want %d", len(jsResults), len(imageGenParityCases))
	}

	for i, c := range imageGenParityCases {
		goResult := coreruntime.ModelSupportsImageGen(c.provider, c.model)
		jsResult := jsResults[i] == "true"
		if goResult != jsResult {
			t.Errorf("parity drift for provider=%q model=%q: Go=%v JS=%v", c.provider, c.model, goResult, jsResult)
		}
	}
}
