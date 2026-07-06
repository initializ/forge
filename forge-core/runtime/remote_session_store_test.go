package runtime

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"sync"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// fakeSessionService is an in-memory stand-in for the platform session
// service (agent-builder). It implements the exact wire contract the
// RemoteSessionStore speaks: monotonic integer ETag per taskID,
// If-None-Match -> 304, If-Match mismatch -> 412. It records enough
// counters for the tests to assert conditional-GET behavior.
type fakeSessionService struct {
	mu       sync.Mutex
	versions map[string]int
	bodies   map[string][]byte

	notModified int // count of 304s served
	fullGets    int // count of 200s served
}

func newFakeSessionService() *fakeSessionService {
	return &fakeSessionService{versions: map[string]int{}, bodies: map[string][]byte{}}
}

func (f *fakeSessionService) taskID(r *http.Request) string { return path.Base(r.URL.Path) }

func (f *fakeSessionService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := f.taskID(r)
	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		ver, ok := f.versions[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if inm := etagUnquote(r.Header.Get("If-None-Match")); inm != "" && inm == strconv.Itoa(ver) {
			f.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		f.fullGets++
		w.Header().Set("ETag", `"`+strconv.Itoa(ver)+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(f.bodies[id])

	case http.MethodPut:
		cur := f.versions[id] // 0 when absent
		if ifm := etagUnquote(r.Header.Get("If-Match")); ifm != "" && ifm != strconv.Itoa(cur) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		next := cur + 1
		f.versions[id] = next
		f.bodies[id] = body
		w.Header().Set("ETag", `"`+strconv.Itoa(next)+`"`)
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(f.versions, id)
		delete(f.bodies, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func newTestRemoteStore(url string) *RemoteSessionStore {
	return NewRemoteSessionStore(RemoteSessionStoreConfig{
		BaseURL:       url,
		AgentID:       "agt-test",
		OrgID:         "org-1",
		PlatformToken: "tok",
	})
}

func sampleSession(taskID, text string) *SessionData {
	return &SessionData{
		TaskID:   taskID,
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: text}},
		Summary:  "s",
	}
}

// TestSessionStore_FileBackendUnchanged asserts the default file backend
// (*MemoryStore) satisfies SessionStore and round-trips unchanged.
func TestSessionStore_FileBackendUnchanged(t *testing.T) {
	var store SessionStore
	ms, err := NewMemoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	store = ms // compile-time proof *MemoryStore implements SessionStore

	in := sampleSession("task-1", "hello")
	if err := store.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load("task-1")
	if err != nil || got == nil {
		t.Fatalf("Load: %v (got %v)", err, got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "hello" {
		t.Fatalf("round-trip mismatch: %+v", got.Messages)
	}
	if err := store.Delete("task-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := store.Load("task-1"); got != nil {
		t.Fatalf("expected nil after delete, got %+v", got)
	}
}

// TestRemoteSessionStore_AnyPodResume: a task saved by pod A resumes on
// pod B (a separate store instance / cold cache) with no shared FS.
func TestRemoteSessionStore_AnyPodResume(t *testing.T) {
	srv := httptest.NewServer(newFakeSessionService())
	defer srv.Close()

	podA := newTestRemoteStore(srv.URL)
	podB := newTestRemoteStore(srv.URL)

	if err := podA.Save(sampleSession("task-42", "from-A")); err != nil {
		t.Fatalf("podA.Save: %v", err)
	}
	got, err := podB.Load("task-42")
	if err != nil {
		t.Fatalf("podB.Load: %v", err)
	}
	if got == nil || len(got.Messages) != 1 || got.Messages[0].Content != "from-A" {
		t.Fatalf("podB did not resume podA's session: %+v", got)
	}
}

// TestRemoteSessionStore_ConditionalGetFreshness: a warm cache issues a
// conditional GET that the service answers 304 (no re-download); when the
// stored version advances, the next Load pulls fresh (200).
func TestRemoteSessionStore_ConditionalGetFreshness(t *testing.T) {
	fake := newFakeSessionService()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	store := newTestRemoteStore(srv.URL)
	if err := store.Save(sampleSession("t", "v1")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Warm-cache Load -> conditional GET -> 304 -> cached copy.
	got, err := store.Load("t")
	if err != nil || got == nil || got.Messages[0].Content != "v1" {
		t.Fatalf("first Load: %v (%+v)", err, got)
	}
	if fake.notModified != 1 {
		t.Fatalf("expected one 304, got %d (fullGets=%d)", fake.notModified, fake.fullGets)
	}

	// Another writer advances the stored version out-of-band.
	other := newTestRemoteStore(srv.URL)
	if _, err := other.Load("t"); err != nil { // prime its If-Match version
		t.Fatalf("other.Load: %v", err)
	}
	if err := other.Save(sampleSession("t", "v2")); err != nil {
		t.Fatalf("other.Save: %v", err)
	}

	// Now our stale If-None-Match no longer matches -> 200 fresh.
	got, err = store.Load("t")
	if err != nil || got == nil || got.Messages[0].Content != "v2" {
		t.Fatalf("second Load did not pull fresh: %v (%+v)", err, got)
	}
}

// TestRemoteSessionStore_SaveConflictOnStaleVersion: a stale writer must
// YIELD on 412, never clobber the concurrent writer's committed turn. Both
// pods load v1; podB commits its turn (v2); podA (still at v1) then tries to
// save and must get ErrConflict — and crucially podB's content must survive
// on the server, unoverwritten.
func TestRemoteSessionStore_SaveConflictOnStaleVersion(t *testing.T) {
	srv := httptest.NewServer(newFakeSessionService())
	defer srv.Close()

	podA := newTestRemoteStore(srv.URL)
	podB := newTestRemoteStore(srv.URL)

	// Both pods take on the same task at v1.
	if err := podA.Save(sampleSession("t", "a1")); err != nil { // v1, podA cached=1
		t.Fatalf("podA.Save: %v", err)
	}
	if _, err := podB.Load("t"); err != nil { // podB cached=1
		t.Fatalf("podB.Load: %v", err)
	}

	// podB commits its turn first -> v2.
	if err := podB.Save(sampleSession("t", "b2")); err != nil {
		t.Fatalf("podB.Save: %v", err)
	}

	// podA is now stale (cached v1). Its Save must yield, not clobber.
	if err := podA.Save(sampleSession("t", "a3")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Save should yield ErrConflict, got %v", err)
	}

	// The concurrent writer's committed turn must survive on the server —
	// a cold reader sees "b2", never podA's clobbering "a3".
	got, err := newTestRemoteStore(srv.URL).Load("t")
	if err != nil || got == nil {
		t.Fatalf("cold Load: %v (got %v)", err, got)
	}
	if got.Messages[0].Content != "b2" {
		t.Fatalf("concurrent writer's turn was clobbered: got %q, want %q", got.Messages[0].Content, "b2")
	}
}

// TestRemoteSessionStore_CloneIsolatesMutableFields pins the cache/caller
// isolation: a ChatMessage's ToolCalls slice must not be shared between the
// caller's snapshot and the store's cached copy (or a shallow clone would let
// an in-place mutation on either side leak across). Regressing cloneSession to
// a shallow element copy fails this.
func TestRemoteSessionStore_CloneIsolatesMutableFields(t *testing.T) {
	srv := httptest.NewServer(newFakeSessionService())
	defer srv.Close()
	store := newTestRemoteStore(srv.URL)

	orig := &SessionData{
		TaskID: "t",
		Messages: []llm.ChatMessage{{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "orig", Arguments: "{}"}},
			},
		}},
	}
	if err := store.Save(orig); err != nil { // caches a clone of orig
		t.Fatalf("Save: %v", err)
	}

	// Mutate the caller's copy AFTER Save — must not leak into the cache.
	orig.Messages[0].ToolCalls[0].Function.Name = "MUTATED"
	orig.Messages[0].ToolCalls = append(orig.Messages[0].ToolCalls, llm.ToolCall{ID: "c2"})

	got, err := store.Load("t") // served from cache -> a fresh clone
	if err != nil || got == nil {
		t.Fatalf("Load: %v (got %v)", err, got)
	}
	if len(got.Messages[0].ToolCalls) != 1 || got.Messages[0].ToolCalls[0].Function.Name != "orig" {
		t.Fatalf("cache shared mutable ToolCalls with the caller: %+v", got.Messages[0].ToolCalls)
	}

	// And a clone handed back by Load must be independently mutable.
	got.Messages[0].ToolCalls[0].Function.Name = "MUTATED2"
	got2, err := store.Load("t")
	if err != nil || got2 == nil {
		t.Fatalf("second Load: %v", err)
	}
	if got2.Messages[0].ToolCalls[0].Function.Name != "orig" {
		t.Fatalf("Load clone shared mutable ToolCalls with the cache: %q", got2.Messages[0].ToolCalls[0].Function.Name)
	}
}
