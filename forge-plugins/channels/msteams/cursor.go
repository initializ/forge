package msteams

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// cursor persists the Microsoft Graph @odata.deltaLink between polls so the
// adapter resumes from the same point across restarts. Writes are atomic
// (write-to-tmp + rename). The file holds a URL only — no secrets — but the
// containing directory is created 0700 to match the rest of `.forge/`.
type cursor struct {
	path string
	mu   sync.Mutex
	val  string
}

type cursorFile struct {
	DeltaLink string `json:"delta_link"`
}

func newCursor(path string) *cursor {
	return &cursor{path: path}
}

// load returns the persisted deltaLink, or "" if no cursor file exists yet.
// A corrupt cursor file is treated as no cursor (the caller will re-init).
func (c *cursor) load() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val != "" {
		return c.val
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		return ""
	}
	var cf cursorFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return ""
	}
	c.val = cf.DeltaLink
	return c.val
}

// save persists deltaLink atomically: write to a tmp file in the same
// directory, then rename(2) into place. Creates the parent directory with
// mode 0700 if missing.
func (c *cursor) save(deltaLink string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("msteams cursor: mkdir: %w", err)
	}

	data, err := json.Marshal(cursorFile{DeltaLink: deltaLink})
	if err != nil {
		return fmt.Errorf("msteams cursor: marshal: %w", err)
	}

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("msteams cursor: write tmp: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("msteams cursor: rename: %w", err)
	}
	c.val = deltaLink
	return nil
}
