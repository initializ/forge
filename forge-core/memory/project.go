package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectID returns the canonical identity for a coding project: the first 12
// hex chars of sha256(normalized git remote). All the ways of naming the same
// repo (scp-style, https, ssh, with/without .git) collapse to one id, so two
// checkouts of the same remote share memory.
//
// When the worktree has no remote (or git is unavailable) it falls back to
// "local-<sha256(abs path)[:12]>" so unrelated local dirs never collapse.
func ProjectID(worktree string) string {
	if remote := gitRemote(worktree); remote != "" {
		return hashID(NormalizeRemote(remote))
	}
	abs, err := filepath.Abs(worktree)
	if err != nil || abs == "" {
		abs = worktree
	}
	return "local-" + hashID(abs)
}

// NormalizeRemote canonicalizes a git remote URL so equivalent forms map to one
// identity. Examples that all normalize to "github.com/initializ/forge":
//
//	git@github.com:initializ/forge.git
//	https://github.com/initializ/forge.git
//	ssh://git@github.com:22/initializ/forge
//	https://user:token@github.com/initializ/forge/
func NormalizeRemote(remote string) string {
	s := strings.ToLower(strings.TrimSpace(remote))

	// Strip URL scheme (https://, ssh://, git://, ...).
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Strip any user@ / user:token@ credentials prefix.
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	// Turn a host:path or host:port/path separator into a plain path separator.
	if i := strings.IndexByte(s, ':'); i >= 0 {
		host, rest := s[:i], s[i+1:]
		if j := strings.IndexByte(rest, '/'); j >= 0 && isAllDigits(rest[:j]) {
			// host:port/path → host/path
			s = host + rest[j:]
		} else {
			// scp-style host:path → host/path
			s = host + "/" + rest
		}
	}

	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	return s
}

// agentIDUnsafe matches any run of characters that are not allowed in a
// filesystem-safe namespace segment.
var agentIDUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// AgentID normalizes a forge.yaml agent_id into a filesystem-safe namespace
// segment (lowercase alphanumerics plus dot, dash, underscore). Empty or
// fully-invalid ids fall back to "unknown-agent".
func AgentID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = agentIDUnsafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if s == "" {
		return "unknown-agent"
	}
	if len(s) > 64 {
		s = strings.Trim(s[:64], "-._")
		if s == "" {
			return "unknown-agent"
		}
	}
	return s
}

// gitRemote returns the origin remote URL of the git repo at worktree, or ""
// when there is none / git is unavailable. Best-effort.
func gitRemote(worktree string) string {
	if worktree == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", worktree, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func hashID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
