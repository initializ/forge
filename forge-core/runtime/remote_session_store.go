package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// remoteStoreTimeout bounds each session-store HTTP call. Session
// state is on the hot path (loaded before a turn, saved after), so the
// budget is tight; on timeout the loop falls back to task.History just
// as it would for a cold session.
const remoteStoreTimeout = 3 * time.Second

// RemoteSessionStore is the opt-in SessionStore backend (issue #243).
// It pushes per-task snapshots to a platform session service over HTTP
// so agent pods stay stateless — any replica can resume any task with
// no shared filesystem / PVC.
//
// Wire contract (the platform session service — agent-builder — is the
// server side):
//
//	GET  {base}/{taskID}?agent_id=<id>   If-None-Match: "<ver>"
//	     -> 200 + ETag: "<ver>" + SessionData   (fresh)
//	     -> 304                                  (caller's cached copy is current)
//	     -> 404                                  (no session yet)
//	PUT  {base}/{taskID}?agent_id=<id>   If-Match: "<ver>" + SessionData
//	     -> 200 + ETag: "<newVer>"               (committed)
//	     -> 412                                   (version conflict / CAS fail)
//	DELETE {base}/{taskID}?agent_id=<id> -> 2xx
//
// Concurrency model (per issue #243): A2A serializes turns within a
// task, so there is normally one writer per task at a time. The store
// keeps the ETag it last saw per task and uses it as the conditional
// header — a conditional GET avoids re-pulling unchanged state (304),
// and an If-Match PUT turns the rare rolling-deploy overlap window into
// a detectable 412 instead of a silent lost update. On 412 the store
// YIELDS — it surfaces ErrConflict rather than re-PUTting its (now stale)
// snapshot, so the concurrent writer's committed turn is never clobbered.
// The loop treats that as a best-effort-persist failure: it logs and moves
// on, the newer state wins, and the model is never re-run. The store's
// cache self-heals on the next turn's conditional GET.
//
// Auth mirrors the admission client exactly: Bearer FORGE_PLATFORM_TOKEN
// plus the Org-Id / Workspace-Id tenancy headers (omitted when empty).
type RemoteSessionStore struct {
	baseURL       string
	agentID       string
	orgID         string
	workspaceID   string
	platformToken string
	client        *http.Client
	logger        Logger

	mu    sync.Mutex
	cache map[string]remoteCacheEntry // taskID -> last-seen version + snapshot
}

type remoteCacheEntry struct {
	version string
	data    *SessionData
}

// RemoteSessionStoreConfig configures a RemoteSessionStore. AgentID is
// required (the platform routes on it). OrgID / WorkspaceID are optional
// (empty -> header omitted). PlatformToken is the reusable Forge ->
// platform bearer (FORGE_PLATFORM_TOKEN), same token the admission
// client sends.
type RemoteSessionStoreConfig struct {
	BaseURL       string
	AgentID       string
	OrgID         string
	WorkspaceID   string
	PlatformToken string
	Logger        Logger
	// HTTPClient overrides the default client (tests inject one).
	HTTPClient *http.Client
}

// NewRemoteSessionStore builds a remote-backed SessionStore. It performs
// no network call at construction time; the first Load/Save hits the
// platform.
func NewRemoteSessionStore(cfg RemoteSessionStoreConfig) *RemoteSessionStore {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: remoteStoreTimeout}
	}
	return &RemoteSessionStore{
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
		agentID:       cfg.AgentID,
		orgID:         cfg.OrgID,
		workspaceID:   cfg.WorkspaceID,
		platformToken: cfg.PlatformToken,
		client:        client,
		logger:        cfg.Logger,
		cache:         make(map[string]remoteCacheEntry),
	}
}

// Load fetches the session for taskID with a conditional GET. A cached
// ETag (from a prior Load/Save on this pod) drives If-None-Match: a 304
// returns the cached snapshot without re-downloading; a 200 refreshes
// the cache; a 404 means no session yet (nil, nil). On any transport /
// server error it returns the error and the loop falls back to
// task.History, exactly as a cold session would.
func (r *RemoteSessionStore) Load(taskID string) (*SessionData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteStoreTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.taskURL(taskID), nil)
	if err != nil {
		return nil, err
	}
	r.setHeaders(req)

	r.mu.Lock()
	cached, hasCache := r.cache[taskID]
	r.mu.Unlock()
	if hasCache && cached.version != "" {
		req.Header.Set("If-None-Match", etagQuote(cached.version))
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("session store GET %s: %w", taskID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		if hasCache {
			return cloneSession(cached.data), nil
		}
		// 304 without a local copy shouldn't happen (we only send
		// If-None-Match when cached); treat as "no data" defensively.
		return nil, nil
	case http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
		var data SessionData
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, fmt.Errorf("session store GET %s: decode: %w", taskID, err)
		}
		r.remember(taskID, etagUnquote(resp.Header.Get("ETag")), &data)
		return cloneSession(&data), nil
	default:
		return nil, fmt.Errorf("session store GET %s: HTTP %d", taskID, resp.StatusCode)
	}
}

// Save commits a full snapshot. It sends If-Match with the last-seen
// version so a concurrent writer's intervening commit is caught as a 412.
// On 412 it YIELDS (returns ErrConflict) rather than re-PUTting: this
// store holds only a full snapshot built from the stale version, so a
// blind retry would overwrite the other writer's committed turn — the
// exact lost update the If-Match exists to detect. A message-level rebase
// isn't available (and is semantically fragile for a conversation), so
// the correct resolution is to let the newer state win. The model is
// never re-run; the cached ETag self-heals on the next conditional GET.
func (r *RemoteSessionStore) Save(data *SessionData) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteStoreTimeout)
	defer cancel()

	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("session store PUT %s: marshal: %w", data.TaskID, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.taskURL(data.TaskID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	r.mu.Lock()
	cached, hasCache := r.cache[data.TaskID]
	r.mu.Unlock()
	if hasCache && cached.version != "" {
		req.Header.Set("If-Match", etagQuote(cached.version))
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("session store PUT %s: %w", data.TaskID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		r.remember(data.TaskID, etagUnquote(resp.Header.Get("ETag")), data)
		return nil
	case http.StatusPreconditionFailed:
		// Drop our stale cache entry so the next Load pulls the winner's
		// state fresh (no stale If-None-Match), then yield.
		r.mu.Lock()
		delete(r.cache, data.TaskID)
		r.mu.Unlock()
		return ErrConflict
	default:
		return fmt.Errorf("session store PUT %s: HTTP %d", data.TaskID, resp.StatusCode)
	}
}

// Delete removes the session for taskID and drops it from the cache.
func (r *RemoteSessionStore) Delete(taskID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteStoreTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, r.taskURL(taskID), nil)
	if err != nil {
		return err
	}
	r.setHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("session store DELETE %s: %w", taskID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	r.mu.Lock()
	delete(r.cache, taskID)
	r.mu.Unlock()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("session store DELETE %s: HTTP %d", taskID, resp.StatusCode)
}

// taskURL builds {base}/{taskID}?agent_id=<id>. The agent ID rides the
// query (the pod serves one agent), mirroring the admission client so
// the platform routes both calls the same way.
func (r *RemoteSessionStore) taskURL(taskID string) string {
	u := r.baseURL + "/" + url.PathEscape(taskID)
	if r.agentID != "" {
		u += "?agent_id=" + url.QueryEscape(r.agentID)
	}
	return u
}

// setHeaders stamps the auth + tenancy headers. Empty tenancy values
// are omitted entirely (never sent as ""), matching the admission
// client so the platform parser distinguishes "unset" from "empty".
func (r *RemoteSessionStore) setHeaders(req *http.Request) {
	if r.platformToken != "" {
		req.Header.Set("Authorization", "Bearer "+r.platformToken)
	}
	if r.orgID != "" {
		req.Header.Set("Org-Id", r.orgID)
	}
	if r.workspaceID != "" {
		req.Header.Set("Workspace-Id", r.workspaceID)
	}
}

func (r *RemoteSessionStore) remember(taskID, version string, data *SessionData) {
	r.mu.Lock()
	r.cache[taskID] = remoteCacheEntry{version: version, data: cloneSession(data)}
	r.mu.Unlock()
}

// etagQuote wraps a bare version in HTTP ETag quotes ("3"); etagUnquote
// reverses it. The platform may send weak ("W/") or strong ETags; we
// only compare the opaque inner value.
func etagQuote(v string) string { return `"` + v + `"` }

func etagUnquote(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "W/")
	return strings.Trim(v, `"`)
}

// cloneSession deep-copies a SessionData so a cached snapshot can't be
// mutated by a caller (and vice-versa). Each ChatMessage's ToolCalls slice
// is copied too — otherwise the clone's messages would share a backing
// array with the original, and an in-place mutation on either side would
// leak across the cache boundary. (ToolCall/FunctionCall are value types
// with only string fields, so copying the slice fully detaches it.)
func cloneSession(d *SessionData) *SessionData {
	if d == nil {
		return nil
	}
	out := *d
	if d.Messages != nil {
		out.Messages = make([]llm.ChatMessage, len(d.Messages))
		for i, m := range d.Messages {
			if m.ToolCalls != nil {
				m.ToolCalls = append([]llm.ToolCall(nil), m.ToolCalls...)
			}
			out.Messages[i] = m
		}
	}
	return &out
}
