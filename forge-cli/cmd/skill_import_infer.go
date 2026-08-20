package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/initializ/forge/forge-skills/parser"
)

// inferredForgeMeta holds signals scanned from a vendored skill's scripts that
// a plain SKILL.md (no metadata.forge) omits.
type inferredForgeMeta struct {
	Bins             []string // interpreters, declared with confidence (python3/node/pip)
	EgressCandidates []string // http(s) hosts found in scripts — REVIEW, may be templated
	EnvCandidates    []string // env-var names read by scripts — REVIEW candidates
}

// empty reports whether nothing worth surfacing was inferred.
func (m inferredForgeMeta) empty() bool {
	return len(m.Bins) == 0 && len(m.EgressCandidates) == 0 && len(m.EnvCandidates) == 0
}

var (
	// egressURLHostRe captures the host of an http(s) URL literal.
	egressURLHostRe = regexp.MustCompile(`https?://([a-zA-Z0-9._-]+)`)
	// pyEnvRe / jsEnvRe capture env-var reads. Shell `$VAR` is intentionally
	// not scanned — it's far too noisy ($1, $PATH, $HOME, …) to be useful.
	pyEnvRe = regexp.MustCompile(`os\.(?:getenv\(|environ(?:\.get)?\(|environ\[)\s*["']([A-Z][A-Z0-9_]{2,})["']`)
	jsEnvRe = regexp.MustCompile(`process\.env\.([A-Z][A-Z0-9_]{2,})`)

	// Shell env reads: $VAR or ${VAR}, uppercase names ≥3 chars (so $1, $a,
	// $IFS-style shorts don't match). Assignments and common shell vars are
	// filtered separately — see shellEnvReads.
	shEnvReadRe = regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]{2,})\}?`)
	// Names assigned locally in the script (so they're a local var, not an env
	// requirement): NAME=, export NAME=, local NAME=, read NAME, for NAME in.
	shEnvAssignRe = regexp.MustCompile(`(?m)(?:^|\bexport\s+|\blocal\s+)([A-Z][A-Z0-9_]{2,})=|(?:\bread\s+(?:-\w+\s+)*|\bfor\s+)([A-Z][A-Z0-9_]{2,})\b`)
)

// commonShellVars are shell/OS-provided variables that a script reads but the
// operator never sets as a skill secret — excluded from env candidates.
var commonShellVars = map[string]bool{
	"PATH": true, "HOME": true, "PWD": true, "OLDPWD": true, "USER": true,
	"SHELL": true, "TERM": true, "LANG": true, "LC_ALL": true, "TMPDIR": true,
	"TMP": true, "TEMP": true, "IFS": true, "PS1": true, "PS2": true,
	"HOSTNAME": true, "RANDOM": true, "SECONDS": true, "LINENO": true,
	"UID": true, "EUID": true, "BASH": true, "BASH_VERSION": true,
	"BASH_SOURCE": true, "FUNCNAME": true, "REPLY": true, "GROUPS": true,
	"SHLVL": true, "COLUMNS": true, "LINES": true, "EDITOR": true, "PAGER": true,
}

// shellEnvReads returns the uppercase env-var names a shell script READS
// ($VAR / ${VAR}) that it does NOT assign locally and that aren't common
// shell/OS vars — i.e. env vars the skill expects to be provided.
func shellEnvReads(text string) []string {
	assigned := map[string]bool{}
	for _, m := range shEnvAssignRe.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			assigned[m[1]] = true
		}
		if m[2] != "" {
			assigned[m[2]] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range shEnvReadRe.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if assigned[name] || commonShellVars[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// inferForgeMeta scans the vendored skill scripts for a plain SKILL.md's
// missing forge metadata (#412): script interpreters → requires.bins, http(s)
// URLs → candidate egress_domains, env reads → candidate requires.env.
//
// Interpreters are declared with confidence (deterministic from extension).
// Egress and env are HEURISTIC — reported as candidates for the author to
// review, never silently declared, because a URL/env in a comment or a
// templated host would otherwise widen egress or invent a secret requirement.
func inferForgeMeta(skillDir string, result *SkillImportResult) inferredForgeMeta {
	var out inferredForgeMeta

	binSet := map[string]bool{}
	for _, s := range result.Scripts {
		switch strings.ToLower(filepath.Ext(s)) {
		case ".py":
			binSet["python3"] = true
		case ".js", ".cjs", ".mjs":
			binSet["node"] = true
		}
	}
	if result.RequirementsTxt {
		binSet["python3"] = true
		binSet["pip"] = true
	}
	out.Bins = sortedKeys(binSet)

	hostSet := map[string]bool{}
	envSet := map[string]bool{}
	for _, s := range result.Scripts {
		body, err := os.ReadFile(filepath.Join(skillDir, filepath.FromSlash(s)))
		if err != nil {
			continue
		}
		text := string(body)
		for _, m := range egressURLHostRe.FindAllStringSubmatch(text, -1) {
			host := strings.Trim(m[1], ".")
			if strings.Contains(host, ".") { // skip bare "api"/"localhost"-ish fragments
				hostSet[host] = true
			}
		}
		ext := strings.ToLower(filepath.Ext(s))
		if ext == ".py" {
			for _, m := range pyEnvRe.FindAllStringSubmatch(text, -1) {
				envSet[m[1]] = true
			}
		}
		if ext == ".js" || ext == ".cjs" || ext == ".mjs" {
			for _, m := range jsEnvRe.FindAllStringSubmatch(text, -1) {
				envSet[m[1]] = true
			}
		}
		if ext == ".sh" || ext == ".bash" {
			for _, name := range shellEnvReads(text) {
				envSet[name] = true
			}
		}
	}
	out.EgressCandidates = sortedKeys(hostSet)
	out.EnvCandidates = sortedKeys(envSet)
	return out
}

// hasForgeMeta reports whether the SKILL.md already declares a metadata.forge
// block (in which case we respect the author and skip inference).
func hasForgeMeta(skillMD string) bool {
	_, meta, err := parser.ParseWithMetadata(strings.NewReader(skillMD))
	if err != nil || meta == nil {
		return false
	}
	return meta.Metadata["forge"] != nil
}

// suggestedForgeMetaYAML renders the inferred metadata as a paste-ready
// frontmatter fragment. Interpreters go under requires.bins; egress/env are
// emitted as COMMENTED candidates so the author opts them in after review.
func suggestedForgeMetaYAML(m inferredForgeMeta) string {
	var b strings.Builder
	b.WriteString("metadata:\n  forge:\n")
	if len(m.Bins) > 0 {
		b.WriteString("    requires:\n      bins:\n")
		for _, bin := range m.Bins {
			fmt.Fprintf(&b, "        - %s\n", bin)
		}
	}
	if len(m.EnvCandidates) > 0 {
		b.WriteString("      # env reads detected in scripts — uncomment the ones the skill needs:\n")
		b.WriteString("      # env:\n      #   required:\n")
		for _, e := range m.EnvCandidates {
			fmt.Fprintf(&b, "      #     - %s\n", e)
		}
	}
	if len(m.EgressCandidates) > 0 {
		b.WriteString("    # egress hosts detected in scripts — uncomment the ones it actually calls:\n")
		b.WriteString("    # egress_domains:\n")
		for _, h := range m.EgressCandidates {
			fmt.Fprintf(&b, "    #   - %s\n", h)
		}
	}
	return b.String()
}

// injectForgeMetaBins splices `requires.bins` (the confident part) into the
// vendored SKILL.md frontmatter when it has no metadata.forge block. Egress/env
// are NOT injected — they're reported as candidates so egress is never widened
// silently (#412 D2). Returns whether it wrote, plus a message.
func injectForgeMetaBins(skillDir string, m inferredForgeMeta) (bool, string) {
	if len(m.Bins) == 0 {
		return false, ""
	}
	path := filepath.Join(skillDir, "SKILL.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, "could not read vendored SKILL.md to write forge metadata: " + err.Error()
	}
	content := string(raw)
	if hasForgeMeta(content) {
		return false, "--write-forge-meta skipped: SKILL.md already has a metadata.forge block — merge the suggested block by hand."
	}
	// Frontmatter must open with `---` on the first line and close at the next
	// `---`. Insert the block just before the closing delimiter.
	if !strings.HasPrefix(content, "---\n") {
		return false, "--write-forge-meta skipped: SKILL.md has no YAML frontmatter to extend."
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false, "--write-forge-meta skipped: SKILL.md frontmatter is not closed with ---."
	}
	// If the frontmatter already has a top-level `metadata:` key (any
	// namespace), appending our own would produce a duplicate key / invalid
	// YAML — merging is the author's call. hasForgeMeta above only catches the
	// forge namespace specifically.
	if frontmatterHasMetadataKey(rest[:end]) {
		return false, "--write-forge-meta skipped: SKILL.md frontmatter already has a metadata: block — add the suggested forge block under it by hand."
	}
	var block strings.Builder
	block.WriteString("metadata:\n  forge:\n    requires:\n      bins:\n")
	for _, bin := range m.Bins {
		fmt.Fprintf(&block, "        - %s\n", bin)
	}
	// front = "---\n" + frontmatter body (up to but not including "\n---")
	front := content[:len("---\n")+end]
	tail := content[len("---\n")+end:] // starts with "\n---"
	newContent := front + "\n" + block.String() + strings.TrimPrefix(tail, "\n")
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return false, "could not write forge metadata into SKILL.md: " + err.Error()
	}
	return true, fmt.Sprintf("wrote requires.bins %v into %s/SKILL.md", m.Bins, filepath.Base(skillDir))
}

var frontmatterMetadataKeyRe = regexp.MustCompile(`(?m)^metadata:`)

// frontmatterHasMetadataKey reports whether the frontmatter body already
// declares a top-level metadata: key.
func frontmatterHasMetadataKey(frontmatter string) bool {
	return frontmatterMetadataKeyRe.MatchString(frontmatter)
}

// dropWarningsContaining removes any warning whose text contains sub.
func dropWarningsContaining(warnings []string, sub string) []string {
	out := warnings[:0]
	for _, w := range warnings {
		if !strings.Contains(w, sub) {
			out = append(out, w)
		}
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
