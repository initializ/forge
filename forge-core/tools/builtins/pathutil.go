package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathValidator provides path confinement to a working directory.
// All resolved paths are guaranteed to be within workDir.
type PathValidator struct {
	workDir string // absolute path
}

// NewPathValidator creates a PathValidator for the given working directory.
func NewPathValidator(workDir string) *PathValidator {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	return &PathValidator{workDir: abs}
}

// Resolve converts a relative or absolute path to an absolute path within workDir.
// It returns an error if the resolved path escapes the working directory.
func (v *PathValidator) Resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return v.workDir, nil
	}

	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Clean(filepath.Join(v.workDir, path))
		// If the path doesn't exist but workspace/<path> does, use that.
		// This handles the common case where the LLM passes "myrepo" instead
		// of "workspace/myrepo" for cloned repositories.
		if _, err := os.Stat(resolved); os.IsNotExist(err) {
			wsPath := filepath.Clean(filepath.Join(v.workDir, "workspace", path))
			if _, wsErr := os.Stat(wsPath); wsErr == nil {
				resolved = wsPath
			}
		}
	}

	// Ensure the resolved path is within workDir.
	if resolved != v.workDir && !strings.HasPrefix(resolved, v.workDir+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the working directory", path)
	}

	return resolved, nil
}

// WorkDir returns the absolute working directory.
func (v *PathValidator) WorkDir() string {
	return v.workDir
}
