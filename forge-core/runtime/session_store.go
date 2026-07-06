package runtime

import "errors"

// ErrConflict is returned by a SessionStore.Save when the store's
// current version no longer matches the version the caller last
// observed — an optimistic-concurrency (compare-and-swap) failure.
// The remote backend attempts one rebase-and-retry before surfacing
// this; a caller that still sees it should treat the persist as
// best-effort-failed (never re-run the LLM), matching the existing
// "failed to persist session" posture.
var ErrConflict = errors.New("session store: version conflict")

// SessionStore persists per-task conversation state (SessionData),
// keyed by task ID. Two backends implement it:
//
//   - file (default): *MemoryStore — today's local .forge/sessions/*.json.
//     Single-pod / dev. Versionless; every Load returns current state.
//   - remote (opt-in): *RemoteSessionStore — pushes snapshots to a
//     platform session service so stateless pods can resume any task on
//     any replica without a PVC (issue #243).
//
// The method set deliberately mirrors *MemoryStore's existing methods
// so the executor and compactor depend only on this interface and the
// file backend satisfies it with no adapter. Version / conditional-GET /
// CAS bookkeeping is an implementation concern of the remote backend
// (it tracks the per-task ETag it last saw), NOT of the caller — the
// loop keeps calling Load/Save/Delete exactly as before.
type SessionStore interface {
	// Load returns the persisted session for taskID, or (nil, nil) when
	// none exists.
	Load(taskID string) (*SessionData, error)
	// Save persists a full snapshot for data.TaskID.
	Save(data *SessionData) error
	// Delete removes the persisted session for taskID (no error if absent).
	Delete(taskID string) error
}

// Compile-time assertion that the file backend satisfies the interface.
var _ SessionStore = (*MemoryStore)(nil)
