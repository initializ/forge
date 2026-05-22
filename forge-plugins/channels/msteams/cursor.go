package msteams

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// cursor persists Microsoft Graph @odata.deltaLink values between polls so
// the adapter resumes from the same point across restarts. Writes are atomic
// (write-to-tmp + rename). The file holds URLs only — no secrets — but the
// containing directory is created 0700 to match the rest of `.forge/`.
//
// The single-cursor API (load/save) is used by the app-only flow which
// polls one global getAllMessages/delta cursor. The per-chat API
// (loadChats/saveChats) is used by the delegated flow which maintains one
// delta cursor per chat. Both can coexist in the same on-disk file.
type cursor struct {
	path string
	mu   sync.Mutex
	val  string                     // global deltaLink (app-only flow)
	chat map[string]chatCursorState // per-chat delta state (delegated flow)
}

type cursorFile struct {
	DeltaLink string `json:"delta_link,omitempty"`

	// Chats holds per-chat delta state for the delegated polling flow.
	// Empty when the app-only flow (single getAllMessages/delta cursor)
	// is in use. The string-valued legacy schema (a bare URL per chat) is
	// also accepted on load for backwards compat with cursor files
	// produced by earlier commits.
	Chats map[string]chatCursorState `json:"chats,omitempty"`
}

// chatCursorState tracks per-chat polling state for the delegated flow.
// Microsoft Graph's v1.0 has no delta primitive for chatMessage in
// delegated context (see graph.go ListChatMessages comment), so we
// list messages directly and track the latest-seen timestamp ourselves.
// Messages with lastModifiedDateTime <= LastSeenTime are skipped.
type chatCursorState struct {
	// LastSeenTime is the lastModifiedDateTime of the newest message
	// already dispatched for this chat, as an RFC3339Nano string. Empty
	// on first contact — the next poll sets it to "now" without
	// dispatching, so the agent only sees messages received AFTER it
	// started running.
	LastSeenTime string `json:"last_seen_time,omitempty"`
}

func newCursor(path string) *cursor {
	return &cursor{path: path}
}

// load returns the persisted global deltaLink (app-only flow), or "" if no
// cursor file exists yet. A corrupt file is treated as no cursor.
func (c *cursor) load() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	return c.val
}

// loadChats returns a copy of the persisted per-chat delta state (delegated
// flow). Returns an empty map if no cursor file exists.
func (c *cursor) loadChats() map[string]chatCursorState {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	out := make(map[string]chatCursorState, len(c.chat))
	for k, v := range c.chat {
		out[k] = v
	}
	return out
}

// loadLocked hydrates val + chat from disk on first access. Subsequent
// calls are no-ops. Caller must hold c.mu.
func (c *cursor) loadLocked() {
	if c.val != "" || c.chat != nil {
		return
	}
	c.chat = map[string]chatCursorState{}
	data, err := os.ReadFile(c.path)
	if err != nil {
		// Both "missing file" and "corrupt/unreadable" map to "empty cursor"
		// — the caller will reinit from "now". Suppress error logging here
		// because the file legitimately doesn't exist on first run.
		return
	}
	var cf cursorFile
	if err := json.Unmarshal(data, &cf); err != nil {
		// Schema may be older — silently treat as empty so the next
		// poll re-bootstraps cleanly. The runtime is more important
		// than backwards compat of an internal state file.
		return
	}
	c.val = cf.DeltaLink
	if cf.Chats != nil {
		c.chat = cf.Chats
	}
}

// save persists the global deltaLink atomically (app-only flow). Per-chat
// state in the file is preserved.
func (c *cursor) save(deltaLink string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	c.val = deltaLink
	return c.writeLocked()
}

// saveChats persists the per-chat delta state atomically (delegated flow).
// The global deltaLink in the file is preserved.
func (c *cursor) saveChats(chats map[string]chatCursorState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	c.chat = make(map[string]chatCursorState, len(chats))
	for k, v := range chats {
		c.chat[k] = v
	}
	return c.writeLocked()
}

// writeLocked persists the current val+chat to disk via tmp+rename. Caller
// must hold c.mu.
func (c *cursor) writeLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("msteams cursor: mkdir: %w", err)
	}

	cf := cursorFile{DeltaLink: c.val}
	if len(c.chat) > 0 {
		cf.Chats = c.chat
	}
	data, err := json.Marshal(cf)
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
	return nil
}
