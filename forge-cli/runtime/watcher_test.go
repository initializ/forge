package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func TestFileWatcher_DetectsChange(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "main.py")
	os.WriteFile(testFile, []byte("# original"), 0644) //nolint:errcheck

	var called atomic.Int32
	logger := coreruntime.NewJSONLogger(&bytes.Buffer{}, false)

	w := NewFileWatcher(dir, func() {
		called.Add(1)
	}, logger)
	// Use shorter interval for testing
	w.interval = 100 * time.Millisecond
	w.debounce = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Watch(ctx)

	// Wait for initial scan
	time.Sleep(200 * time.Millisecond)

	// Modify the file
	os.WriteFile(testFile, []byte("# modified"), 0644) //nolint:errcheck

	// Wait for detection
	time.Sleep(500 * time.Millisecond)

	if called.Load() == 0 {
		t.Error("onChange was not called after file modification")
	}
}

func TestFileWatcher_IgnoresHiddenDirs(t *testing.T) {
	dir := t.TempDir()

	// Create a file in .git directory — should be ignored
	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)                                           //nolint:errcheck
	os.WriteFile(filepath.Join(gitDir, "config.py"), []byte("x"), 0644) //nolint:errcheck

	// Create a watched file
	os.WriteFile(filepath.Join(dir, "main.py"), []byte("ok"), 0644) //nolint:errcheck

	var called atomic.Int32
	logger := coreruntime.NewJSONLogger(&bytes.Buffer{}, false)

	w := NewFileWatcher(dir, func() {
		called.Add(1)
	}, logger)
	w.interval = 100 * time.Millisecond
	w.debounce = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Watch(ctx)
	time.Sleep(200 * time.Millisecond)

	// Modify .git file — should NOT trigger callback
	os.WriteFile(filepath.Join(gitDir, "config.py"), []byte("modified"), 0644) //nolint:errcheck
	time.Sleep(300 * time.Millisecond)

	if called.Load() != 0 {
		t.Error("onChange should not be called for changes in .git directory")
	}
}

func TestFileWatcher_Debounce(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("v1"), 0644) //nolint:errcheck

	var called atomic.Int32
	logger := coreruntime.NewJSONLogger(&bytes.Buffer{}, false)

	w := NewFileWatcher(dir, func() {
		called.Add(1)
	}, logger)
	w.interval = 100 * time.Millisecond
	w.debounce = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Watch(ctx)
	time.Sleep(200 * time.Millisecond)

	// Rapid changes should be debounced
	for i := range 5 {
		os.WriteFile(filepath.Join(dir, "app.py"), []byte("v"+string(rune('2'+i))), 0644) //nolint:errcheck
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(500 * time.Millisecond)

	// The callback should have been called at least once
	count := called.Load()
	if count == 0 {
		t.Error("expected at least one onChange call")
	}
}
