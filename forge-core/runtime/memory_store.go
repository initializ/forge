package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// SessionData holds the persisted state for a single task's conversation.
type SessionData struct {
	TaskID    string            `json:"task_id"`
	Messages  []llm.ChatMessage `json:"messages"`
	Summary   string            `json:"summary,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// MemoryStore provides file-backed session persistence.
// Each session is stored as a JSON file in the configured directory.
type MemoryStore struct {
	dir string
	mu  sync.RWMutex
}

// sanitizeRe matches characters unsafe for filenames.
var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_\-.]`)

// NewMemoryStore creates a MemoryStore backed by the given directory.
// The directory is created if it does not exist.
func NewMemoryStore(dir string) (*MemoryStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating sessions dir: %w", err)
	}
	return &MemoryStore{dir: dir}, nil
}

// Save persists a SessionData to disk using atomic write (temp+fsync+rename).
// On the first write for a task, CreatedAt is set to now. On subsequent writes,
// the original CreatedAt is preserved from the existing file.
func (s *MemoryStore) Save(data *SessionData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fname := s.filename(data.TaskID)

	// Preserve CreatedAt from existing file if present.
	if data.CreatedAt.IsZero() {
		existing, _ := s.loadLocked(data.TaskID)
		if existing != nil && !existing.CreatedAt.IsZero() {
			data.CreatedAt = existing.CreatedAt
		} else {
			data.CreatedAt = time.Now().UTC()
		}
	}
	data.UpdatedAt = time.Now().UTC()

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session data: %w", err)
	}

	// Atomic write: write to temp file, fsync, rename.
	tmpFile := fname + ".tmp"
	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}

	if _, err := f.Write(raw); err != nil {
		f.Close()          //nolint:errcheck
		os.Remove(tmpFile) //nolint:errcheck
		return fmt.Errorf("writing temp file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()          //nolint:errcheck
		os.Remove(tmpFile) //nolint:errcheck
		return fmt.Errorf("syncing temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpFile) //nolint:errcheck
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpFile, fname); err != nil {
		os.Remove(tmpFile) //nolint:errcheck
		return fmt.Errorf("renaming temp file: %w", err)
	}

	return nil
}

// Load reads a SessionData from disk. Returns (nil, nil) if the session file
// does not exist.
func (s *MemoryStore) Load(taskID string) (*SessionData, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadLocked(taskID)
}

// loadLocked reads a session file without acquiring locks (caller must hold lock).
func (s *MemoryStore) loadLocked(taskID string) (*SessionData, error) {
	fname := s.filename(taskID)
	raw, err := os.ReadFile(fname)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading session file: %w", err)
	}

	var data SessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("unmarshaling session data: %w", err)
	}
	return &data, nil
}

// List returns all session task IDs stored on disk.
func (s *MemoryStore) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matches, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}

	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		id := strings.TrimSuffix(base, ".json")
		ids = append(ids, id)
	}
	return ids, nil
}

// Delete removes a session file from disk.
func (s *MemoryStore) Delete(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fname := s.filename(taskID)
	if err := os.Remove(fname); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting session file: %w", err)
	}
	return nil
}

// Cleanup removes sessions older than maxAge based on their UpdatedAt timestamp.
// Returns the number of sessions deleted.
func (s *MemoryStore) Cleanup(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	matches, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return 0, fmt.Errorf("listing sessions for cleanup: %w", err)
	}

	cutoff := time.Now().UTC().Add(-maxAge)
	deleted := 0

	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}

		var data SessionData
		if err := json.Unmarshal(raw, &data); err != nil {
			continue
		}

		if !data.UpdatedAt.IsZero() && data.UpdatedAt.Before(cutoff) {
			if err := os.Remove(m); err == nil {
				deleted++
			}
		}
	}

	return deleted, nil
}

// filename returns the file path for a given task ID.
func (s *MemoryStore) filename(taskID string) string {
	safe := sanitizeTaskID(taskID)
	return filepath.Join(s.dir, safe+".json")
}

// sanitizeTaskID replaces characters unsafe for filenames.
func sanitizeTaskID(id string) string {
	return sanitizeRe.ReplaceAllString(id, "_")
}
