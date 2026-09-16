package optimizer

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"regexp"
	"strings"
)

// Per-session repo scoping. Memory must be scoped to the repo the SESSION is
// working in, not the directory the optimizer was launched from — one proxy
// serves sessions across many repos. Claude Code includes the working directory
// in its system prompt; we extract it and resolve it to a (repo, commit).

// workingDirRe matches the "Working directory: <path>" line Claude Code emits in
// its environment block (case-insensitive, path runs to end of line).
var workingDirRe = regexp.MustCompile(`(?i)working directory:\s*(.+)`)

// workingDirFromBody extracts the session's working directory from the request's
// system prompt, or "" if absent.
func workingDirFromBody(body []byte) string {
	_, system := modelAndSystem(body)
	if system == "" {
		return ""
	}
	m := workingDirRe.FindStringSubmatch(system)
	if len(m) < 2 {
		return ""
	}
	cwd := strings.TrimSpace(m[1])
	cwd = strings.Trim(cwd, "`'\"<>) \t")
	return cwd
}

// resolveRepo maps a request to its (repo, commit), scoping memory per session.
// It memoizes per working directory. Falls back to the launch-time repo/commit
// when the request carries no working directory.
func (f *MemoryFormer) resolveRepo(body []byte) (string, string) {
	cwd := workingDirFromBody(body)
	if cwd == "" {
		return f.repo, f.commit
	}
	f.mu.Lock()
	if v, ok := f.repoCache[cwd]; ok {
		f.mu.Unlock()
		return v[0], v[1]
	}
	f.mu.Unlock()

	var repo, commit string
	if f.repoResolver != nil {
		repo, commit = f.repoResolver(cwd)
	}
	if repo == "" {
		// No git resolver → basename of the working dir, disambiguated by a short
		// hash of the FULL path so distinct checkouts that share a basename
		// (~/a/client vs ~/work/client) don't collapse to one scope and
		// cross-inject each other's memory.
		repo = filepath.Base(cwd) + "-" + shortPathHash(cwd)
	}

	f.mu.Lock()
	f.repoCache[cwd] = [2]string{repo, commit}
	f.mu.Unlock()
	return repo, commit
}

// shortPathHash returns a 6-hex-char FNV-1a digest of a path, used only to
// disambiguate same-basename working directories (not security-sensitive).
func shortPathHash(p string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(p))
	return fmt.Sprintf("%08x", h.Sum32())[:6]
}
