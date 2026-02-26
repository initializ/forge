package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

func TestMemoryStoreSaveLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	data := &SessionData{
		TaskID: "task-1",
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Content: "hello"},
			{Role: llm.RoleAssistant, Content: "hi there"},
		},
		Summary: "greeting exchange",
	}

	if err := store.Save(data); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("task-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil for existing session")
	}

	if loaded.TaskID != "task-1" {
		t.Errorf("expected task ID 'task-1', got %q", loaded.TaskID)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if loaded.Summary != "greeting exchange" {
		t.Errorf("expected summary 'greeting exchange', got %q", loaded.Summary)
	}
	if loaded.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestMemoryStoreLoadNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	loaded, err := store.Load("nonexistent")
	if err != nil {
		t.Fatalf("Load should not error for missing session: %v", err)
	}
	if loaded != nil {
		t.Error("Load should return nil for missing session")
	}
}

func TestMemoryStoreList(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	// Save two sessions
	for _, id := range []string{"task-a", "task-b"} {
		if err := store.Save(&SessionData{
			TaskID:   id,
			Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 session IDs, got %d", len(ids))
	}

	// Check both IDs are present
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found["task-a"] || !found["task-b"] {
		t.Errorf("expected task-a and task-b, got %v", ids)
	}
}

func TestMemoryStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	if err := store.Save(&SessionData{
		TaskID:   "task-del",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "bye"}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete("task-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	loaded, err := store.Load("task-del")
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if loaded != nil {
		t.Error("session should be deleted")
	}
}

func TestMemoryStoreCleanup(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	// Write an old session file directly (Save always sets UpdatedAt to now).
	oldData := &SessionData{
		TaskID:    "old-task",
		Messages:  []llm.ChatMessage{{Role: llm.RoleUser, Content: "old"}},
		CreatedAt: time.Now().UTC().Add(-10 * 24 * time.Hour),
		UpdatedAt: time.Now().UTC().Add(-10 * 24 * time.Hour),
	}
	raw, _ := json.Marshal(oldData)
	if err := os.WriteFile(filepath.Join(dir, "old-task.json"), raw, 0o644); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}

	// Save a recent session
	recent := &SessionData{
		TaskID:   "recent-task",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "new"}},
	}
	if err := store.Save(recent); err != nil {
		t.Fatalf("Save recent: %v", err)
	}

	// Cleanup with 7-day TTL
	deleted, err := store.Cleanup(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Old should be gone, recent should remain
	oldLoaded, _ := store.Load("old-task")
	if oldLoaded != nil {
		t.Error("old session should be cleaned up")
	}

	recentLoaded, _ := store.Load("recent-task")
	if recentLoaded == nil {
		t.Error("recent session should not be cleaned up")
	}
}

func TestMemoryStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	if err := store.Save(&SessionData{
		TaskID:   "atomic-test",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "test"}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify no .tmp file remains
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(matches) > 0 {
		t.Errorf("temp file should be cleaned up, found: %v", matches)
	}

	// Verify the final file exists
	matches, _ = filepath.Glob(filepath.Join(dir, "*.json"))
	if len(matches) != 1 {
		t.Errorf("expected 1 json file, got %d", len(matches))
	}
}

func TestMemoryStoreConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			data := &SessionData{
				TaskID:   "concurrent-task",
				Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "msg"}},
			}
			_ = store.Save(data)
			_, _ = store.Load("concurrent-task")
		}(i)
	}
	wg.Wait()

	// Verify the file is valid after concurrent writes
	loaded, err := store.Load("concurrent-task")
	if err != nil {
		t.Fatalf("Load after concurrent writes: %v", err)
	}
	if loaded == nil {
		t.Error("session should exist after concurrent writes")
	}
}

func TestMemoryStoreSanitizeTaskID(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	// Task ID with special characters
	data := &SessionData{
		TaskID:   "task/with:special chars!",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "test"}},
	}
	if err := store.Save(data); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("task/with:special chars!")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Error("should be able to load session with special chars in ID")
	}

	// Verify no special characters in filename
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == ".tmp" {
			continue
		}
		name := e.Name()
		for _, c := range name {
			if c == '/' || c == ':' || c == '!' || c == ' ' {
				t.Errorf("filename contains unsafe character %q: %s", string(c), name)
			}
		}
	}
}

func TestMemoryStorePreservesCreatedAt(t *testing.T) {
	dir := t.TempDir()
	store, err := NewMemoryStore(dir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	// First save
	if err := store.Save(&SessionData{
		TaskID:   "created-at-test",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "first"}},
	}); err != nil {
		t.Fatalf("Save 1: %v", err)
	}

	first, _ := store.Load("created-at-test")
	originalCreatedAt := first.CreatedAt

	// Second save — CreatedAt should be preserved
	if err := store.Save(&SessionData{
		TaskID:   "created-at-test",
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: "second"}},
	}); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	second, _ := store.Load("created-at-test")
	if !second.CreatedAt.Equal(originalCreatedAt) {
		t.Errorf("CreatedAt changed: was %v, now %v", originalCreatedAt, second.CreatedAt)
	}
	if second.Messages[0].Content != "second" {
		t.Error("Messages should be updated")
	}
}
