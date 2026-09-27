package memory

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile writes data to path atomically: it writes to a temp file in
// the same directory, fsyncs it, then renames it over the destination. The
// rename is atomic on POSIX filesystems, so a reader (or a crash mid-write)
// never observes a partially written file. The parent directory is fsynced
// best-effort so the rename itself is durable.
//
// All card, procedure, session, and registry writes go through this helper —
// the FileVectorStore flush is the one pre-existing temp→rename path (without
// fsync), and it is left as-is.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds. After a
	// successful rename this simply fails silently (the name is gone).
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}

	// Best-effort: fsync the directory so the rename survives a crash.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
