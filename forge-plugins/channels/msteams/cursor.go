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

// chatCursorState tracks both the next URL to call and whether we've passed
// the initial-sync phase for a chat. Graph's /chats/{id}/messages/delta
// returns full chat history on the first call, paginated until a deltaLink
// arrives. We mustn't dispatch those historical messages — only changes
// returned by calls made AFTER the first deltaLink count as real-time
// chat activity.
type chatCursorState struct {
	URL string `json:"url"`
	// DeltaSeen flips true the first time Graph returns @odata.deltaLink
	// for this chat. While false, messages returned by polling are
	// historical (initial sync) and the dispatch loop drops them.
	DeltaSeen bool `json:"delta_seen,omitempty"`
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
		// Try the legacy schema (map[string]string) before giving up — old
		// cursor files used a bare URL per chat.
		var legacy struct {
			DeltaLink string            `json:"delta_link,omitempty"`
			Chats     map[string]string `json:"chats,omitempty"`
		}
		if err2 := json.Unmarshal(data, &legacy); err2 == nil {
			c.val = legacy.DeltaLink
			for k, v := range legacy.Chats {
				// Legacy cursors are treated as initial-sync state: we
				// haven't seen a deltaLink yet from THIS process, so the
				// next call will re-do initial sync. That's safer than
				// inadvertently dispatching the next page as new traffic.
				c.chat[k] = chatCursorState{URL: v, DeltaSeen: false}
			}
		}
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
