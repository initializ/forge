package uiconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// EnvFileName is the workspace-level .env file the UI process consults
// for secret values (per-key API tokens, etc.) configured via Settings.
// Lives under <workspace>/.forge/.env so it's grouped with ui.yaml and
// auto-protected by the .forge/.gitignore the loader writes next to it.
const EnvFileName = ".env"

// EnvLookupForWorkspace returns a lookup function suitable for passing
// to LoadSkillBuilderLLM. The workspace .env takes precedence over the
// OS environment for any key it defines; otherwise OS env is consulted.
// Missing file → OS env only (a no-op layer).
//
// The function takes a fresh snapshot at call time — Settings writes
// are picked up by the next request without restarting forge ui.
func EnvLookupForWorkspace(workspaceDir string) func(string) string {
	path := filepath.Join(workspaceDir, WorkspaceConfigDir, EnvFileName)
	fileEnv, _ := readDotEnv(path) // missing file → empty map; ignore error
	return func(name string) string {
		if v, ok := fileEnv[name]; ok {
			return v
		}
		return os.Getenv(name)
	}
}

// SetEnvFileValue writes (or updates) a single KEY=VALUE pair in the
// workspace .env file. Atomic via temp-file + rename; permissions 0600
// so the file is readable only by the owning user. Creates the parent
// .forge/ directory if missing.
//
// Lines other than the target key are preserved verbatim — including
// formatting, comments, and the file's trailing newline (or absence
// thereof). This is the "edit-in-place" guarantee operators expect
// from a Settings UI.
//
// If value is the empty string, the key is REMOVED from the file (so
// the operator can clear a credential without leaving a dangling
// KEY= line).
func SetEnvFileValue(workspaceDir, name, value string) error {
	if name == "" {
		return fmt.Errorf("env key name is required")
	}
	dir := filepath.Join(workspaceDir, WorkspaceConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, EnvFileName)

	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	updated, replaced := mutateEnvLines(raw, name, value)
	if !replaced && value != "" {
		// New key — append with a leading newline if the file is
		// non-empty and doesn't already end in one.
		if len(updated) > 0 && updated[len(updated)-1] != '\n' {
			updated = append(updated, '\n')
		}
		updated = append(updated, []byte(fmt.Sprintf("%s=%s\n", name, value))...)
	}

	// Atomic write via temp file in the same directory.
	tmp, err := os.CreateTemp(dir, ".env.tmp.*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once rename succeeds

	if _, err := tmp.Write(updated); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming to %s: %w", path, err)
	}

	// Best-effort gitignore so the file isn't accidentally committed.
	// We DO NOT touch a workspace-level .gitignore (that's the
	// operator's repo); we write our own .forge/.gitignore which is
	// scoped to the .forge/ directory we own.
	_ = writeForgeGitignore(dir)
	return nil
}

// mutateEnvLines edits raw .env bytes: replaces the line that begins
// with "<name>=" with "<name>=<value>", or removes it entirely when
// value is empty. Returns the new bytes and a flag indicating whether
// the key was found in the file (so the caller can decide to append).
func mutateEnvLines(raw []byte, name, value string) (out []byte, replaced bool) {
	prefix := []byte(name + "=")
	lines := bytes.SplitAfter(raw, []byte("\n"))
	var buf bytes.Buffer
	for _, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trimmed, prefix) {
			replaced = true
			if value == "" {
				continue // delete
			}
			buf.WriteString(name)
			buf.WriteString("=")
			buf.WriteString(value)
			// Preserve trailing newline if the original line had one.
			if bytes.HasSuffix(line, []byte("\n")) {
				buf.WriteByte('\n')
			}
			continue
		}
		buf.Write(line)
	}
	return buf.Bytes(), replaced
}

// writeForgeGitignore makes sure <workspace>/.forge/.gitignore exists
// and contains ".env" so the secret file is auto-protected. Idempotent.
func writeForgeGitignore(forgeDir string) error {
	path := filepath.Join(forgeDir, ".gitignore")
	const want = ".env\n"
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Contains(existing, []byte(".env")) {
			return nil // already protected
		}
		// Append our line, preserving operator additions.
		combined := existing
		if len(combined) > 0 && combined[len(combined)-1] != '\n' {
			combined = append(combined, '\n')
		}
		combined = append(combined, want...)
		return os.WriteFile(path, combined, 0o644)
	}
	return os.WriteFile(path, []byte(want), 0o644)
}

// SortedEnvFileKeys returns the keys present in the workspace .env,
// sorted. Useful for tests + diagnostics; not used in the hot path.
func SortedEnvFileKeys(workspaceDir string) []string {
	path := filepath.Join(workspaceDir, WorkspaceConfigDir, EnvFileName)
	m, _ := readDotEnv(path)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
