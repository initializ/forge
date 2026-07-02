// Command reportgen turns `go test -json` output from the OWASP ASI
// conformance suite into a legible per-entry report (conformance.md +
// conformance.json). It is deterministic and reads no clock/network.
//
// Usage: reportgen <gotest-json-file> <out-dir>
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// entry holds the static grade + backlog linkage for one ASI id; runtime
// results (status, measured lines) are merged in from the test output.
type entry struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Grade  string `json:"grade"`
	Issues string `json:"issues"`
}

var entries = []entry{
	{"ASI01", "Agent Goal Hijack", "Partial", "#229"},
	{"ASI02", "Tool Misuse & Exploitation", "Enforced", "#223,#231,#235"},
	{"ASI03", "Identity & Privilege Abuse", "Partial", "#232"},
	{"ASI04", "Agentic Supply Chain", "Partial", "#227,#228"},
	{"ASI05", "Unexpected Code Execution", "Enforced", "#234"},
	{"ASI06", "Memory & Context Poisoning", "Partial", "#225"},
	{"ASI07", "Insecure Inter-Agent Comms", "Partial", "#226"},
	{"ASI08", "Cascading Failures", "Partial", "#233"},
	{"ASI09", "Human-Agent Trust Exploitation", "Partial", "#223"},
	{"ASI10", "Rogue Agents", "Partial", "#224,#230"},
}

type testEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

type result struct {
	Pass, Fail, Skip int
	Measured         []string
}

var (
	asiRe     = regexp.MustCompile(`^TestASI(\d\d)`)
	measureRe = regexp.MustCompile(`(rate:.*|known-gap.*|residual.*|denied \d+)`)
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: reportgen <gotest-json-file> <out-dir>")
		os.Exit(2)
	}
	inPath, outDir := os.Args[1], os.Args[2]

	f, err := os.Open(inPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", inPath, err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()

	byID := map[string]*result{}
	for _, e := range entries {
		byID[e.ID] = &result{}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var ev testEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Test == "" {
			continue
		}
		m := asiRe.FindStringSubmatch(ev.Test)
		if m == nil {
			continue
		}
		id := "ASI" + m[1]
		r := byID[id]
		if r == nil {
			continue
		}
		switch ev.Action {
		case "pass":
			r.Pass++
		case "fail":
			r.Fail++
		case "skip":
			r.Skip++
		case "output":
			if line := measureRe.FindString(ev.Output); line != "" {
				r.Measured = append(r.Measured, strings.TrimSpace(line))
			}
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}
	writeMarkdown(outDir, byID)
	writeJSON(outDir, byID)
	fmt.Printf("wrote %s/conformance.md and conformance.json\n", outDir)
}

func writeMarkdown(outDir string, byID map[string]*result) {
	var b strings.Builder
	b.WriteString("# OWASP ASI Conformance Report\n\n")
	b.WriteString("Generated from the instrumented conformance suite ")
	b.WriteString("(`tests/owasp-asi`). Grades come from ")
	b.WriteString("`docs/security/owasp-asi-conformance.md`; pass/skip/fail and ")
	b.WriteString("measured rates come from the test run. Skips are tracked ")
	b.WriteString("backlog gaps, not build breakers.\n\n")
	b.WriteString("| Entry | Title | Grade | Pass | xfail | Fail | Backlog | Measured |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, e := range entries {
		r := byID[e.ID]
		measured := strings.Join(dedupe(r.Measured), "; ")
		if measured == "" {
			measured = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %s | %s |\n",
			e.ID, e.Title, e.Grade, r.Pass, r.Skip, r.Fail, e.Issues, measured)
	}
	b.WriteString("\nxfail = `t.Skip` naming a backlog issue; a green run has Fail=0 across all entries.\n")
	_ = os.WriteFile(filepath.Join(outDir, "conformance.md"), []byte(b.String()), 0o644)
}

func writeJSON(outDir string, byID map[string]*result) {
	type row struct {
		entry
		Pass     int      `json:"pass"`
		Xfail    int      `json:"xfail"`
		Fail     int      `json:"fail"`
		Measured []string `json:"measured"`
	}
	var rows []row
	for _, e := range entries {
		r := byID[e.ID]
		rows = append(rows, row{e, r.Pass, r.Skip, r.Fail, dedupe(r.Measured)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	data, _ := json.MarshalIndent(map[string]any{"entries": rows}, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, "conformance.json"), data, 0o644)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
