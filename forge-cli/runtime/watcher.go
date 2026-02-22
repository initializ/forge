package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// FileWatcher polls the filesystem for changes and invokes a callback.
type FileWatcher struct {
	dir        string
	onChange   func()
	logger     coreruntime.Logger
	interval   time.Duration
	debounce   time.Duration
	mu         sync.Mutex
	lastModMap map[string]time.Time
}

// NewFileWatcher creates a watcher that polls dir every 2s for changes in
// watched file types. onChange is called (debounced) when changes are detected.
func NewFileWatcher(dir string, onChange func(), logger coreruntime.Logger) *FileWatcher {
	return &FileWatcher{
		dir:        dir,
		onChange:   onChange,
		logger:     logger,
		interval:   2 * time.Second,
		debounce:   500 * time.Millisecond,
		lastModMap: make(map[string]time.Time),
	}
}

var watchedExtensions = map[string]bool{
	".py": true, ".go": true, ".ts": true, ".js": true, ".yaml": true, ".yml": true,
}

var skippedDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true,
	".forge-output": true, "venv": true, ".venv": true,
}

// Watch starts polling until ctx is cancelled. It blocks.
func (w *FileWatcher) Watch(ctx context.Context) {
	// Build initial snapshot
	w.mu.Lock()
	w.lastModMap = w.scan()
	w.mu.Unlock()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.detectChanges() {
				w.logger.Info("file change detected, reloading", nil)
				// Debounce: wait briefly for batched writes
				time.Sleep(w.debounce)
				w.onChange()
			}
		}
	}
}

func (w *FileWatcher) scan() map[string]time.Time {
	modMap := make(map[string]time.Time)
	_ = filepath.WalkDir(w.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !watchedExtensions[ext] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		modMap[path] = info.ModTime()
		return nil
	})
	return modMap
}

func (w *FileWatcher) detectChanges() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	current := w.scan()
	changed := false

	// Check for new or modified files
	for path, modTime := range current {
		if prev, ok := w.lastModMap[path]; !ok || !modTime.Equal(prev) {
			changed = true
			break
		}
	}

	// Check for deleted files
	if !changed {
		for path := range w.lastModMap {
			if _, ok := current[path]; !ok {
				changed = true
				break
			}
		}
	}

	if changed {
		w.lastModMap = current
	}
	return changed
}
