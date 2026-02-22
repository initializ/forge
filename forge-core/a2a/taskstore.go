package a2a

import (
	"encoding/json"
	"sync"
)

// TaskStore is a thread-safe in-memory store for A2A tasks.
type TaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

// NewTaskStore creates an empty TaskStore.
func NewTaskStore() *TaskStore {
	return &TaskStore{tasks: make(map[string]*Task)}
}

// Get returns a deep copy of the task with the given ID, or nil if not found.
func (s *TaskStore) Get(id string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil
	}
	return deepCopyTask(t)
}

// Put stores a task. It overwrites any existing task with the same ID.
func (s *TaskStore) Put(t *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = deepCopyTask(t)
}

// UpdateStatus updates the status of an existing task. Returns false if the
// task does not exist.
func (s *TaskStore) UpdateStatus(id string, status TaskStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return false
	}
	t.Status = status
	return true
}

// SetArtifacts replaces the artifacts for an existing task. Returns false if
// the task does not exist.
func (s *TaskStore) SetArtifacts(id string, artifacts []Artifact) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return false
	}
	t.Artifacts = artifacts
	return true
}

// deepCopyTask creates a deep copy by JSON round-tripping.
func deepCopyTask(t *Task) *Task {
	data, _ := json.Marshal(t)
	var copy Task
	json.Unmarshal(data, &copy) //nolint:errcheck
	return &copy
}
