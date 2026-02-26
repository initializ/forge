// Package memory provides long-term agent memory with file-based storage,
// vector search, and hybrid retrieval.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileStore manages the on-disk memory directory (.forge/memory).
type FileStore struct {
	dir string
}

// NewFileStore creates a FileStore rooted at dir, creating it if needed.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating memory dir: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// Dir returns the root directory of the file store.
func (fs *FileStore) Dir() string { return fs.dir }

// ReadFile reads a file relative to the memory directory.
// Returns an error if the path escapes the memory directory.
func (fs *FileStore) ReadFile(relPath string) (string, error) {
	absPath, err := fs.safePath(relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// AppendDaily appends an entry to today's daily log (YYYY-MM-DD.md).
func (fs *FileStore) AppendDaily(entry string) error {
	filename := time.Now().UTC().Format("2006-01-02") + ".md"
	path := filepath.Join(fs.dir, filename)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening daily log: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Add timestamp prefix and trailing newline.
	ts := time.Now().UTC().Format("15:04:05")
	_, err = fmt.Fprintf(f, "\n## %s\n%s\n", ts, entry)
	return err
}

// ListFiles returns all .md files in the memory directory (relative paths).
func (fs *FileStore) ListFiles() ([]string, error) {
	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".md") {
			files = append(files, e.Name())
		}
	}
	return files, nil
}

// EnsureMemoryMD creates a template MEMORY.md if one doesn't exist.
func (fs *FileStore) EnsureMemoryMD() error {
	path := filepath.Join(fs.dir, "MEMORY.md")
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}

	template := `# Agent Memory

This file contains curated long-term facts and knowledge.
Entries here are evergreen — they do not decay over time.

## Key Facts

<!-- Add important facts, user preferences, and project context here -->
`
	return os.WriteFile(path, []byte(template), 0o644)
}

// safePath resolves a relative path and validates it stays within the memory dir.
func (fs *FileStore) safePath(relPath string) (string, error) {
	// Clean the path to prevent traversal
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("absolute paths not allowed: %s", relPath)
	}
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("path traversal not allowed: %s", relPath)
	}

	absPath := filepath.Join(fs.dir, cleaned)

	// Double-check the resolved path is within our dir
	absDir, err := filepath.Abs(fs.dir)
	if err != nil {
		return "", err
	}
	absResolved, err := filepath.Abs(absPath)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absResolved, absDir+string(filepath.Separator)) && absResolved != absDir {
		return "", fmt.Errorf("path escapes memory directory: %s", relPath)
	}

	return absPath, nil
}
