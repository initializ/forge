package whatsapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"

	// Pure-Go SQLite driver, registered as "sqlite". Deliberately NOT
	// mattn/go-sqlite3: the release image builds with CGO_ENABLED=0 (see the
	// repo Dockerfile), under which a cgo driver compiles but panics at
	// sql.Open with "unknown driver". modernc has no cgo dependency.
	_ "modernc.org/sqlite"
)

// sqlDialect is the driver name modernc.org/sqlite registers with database/sql.
// whatsmeow's dbutil matches any dialect with the "sqlite" prefix, so this
// selects SQLite SQL generation as well as the driver.
const sqlDialect = "sqlite"

// sessionDSN builds the connection string for the pairing store.
//
// foreign_keys is enabled because whatsmeow's schema relies on cascading
// deletes to clean up per-device signal state; without it a logout leaves
// orphaned rows that make the next pairing fail. Note the pragma syntax is
// modernc's (`_pragma=foreign_keys(1)`), not mattn's (`_foreign_keys=on`) —
// mattn's form is silently ignored by this driver.
func sessionDSN(path string) string {
	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
}

// openContainer opens (creating if absent) the whatsmeow session store at
// path and runs any pending schema upgrades.
func openContainer(ctx context.Context, path string) (*sqlstore.Container, error) {
	if path == "" {
		return nil, fmt.Errorf("whatsapp: session_path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("whatsapp: creating session directory %s: %w", dir, err)
		}
	}
	container, err := sqlstore.New(ctx, sqlDialect, sessionDSN(path), nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: opening session store %s: %w", path, err)
	}

	// The store holds the pairing: anyone who can read it can send messages as
	// the linked account. SQLite creates the file under the process umask,
	// which on a default system is world-readable — tighten it to owner-only.
	// Best-effort: a store on a filesystem without POSIX modes (a mounted
	// volume, Windows) is still usable, so a failure here must not block
	// startup.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return container, nil //nolint:nilerr // hardening is best-effort; see comment above
	}
	return container, nil
}

// loadPairedDevice returns the paired device from the session store at path.
//
// It fails when the store holds no completed pairing. Pairing requires a human
// to scan a QR code, so it cannot happen inside `forge run` — the adapter
// refuses to start rather than blocking a server boot on a terminal
// interaction that may never come.
func loadPairedDevice(ctx context.Context, path string) (*sqlstore.Container, *store.Device, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("whatsapp: no session at %s — run `forge channel whatsapp-login` to pair", path)
		}
		return nil, nil, fmt.Errorf("whatsapp: stat session %s: %w", path, err)
	}

	container, err := openContainer(ctx, path)
	if err != nil {
		return nil, nil, err
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, nil, fmt.Errorf("whatsapp: reading device from %s: %w", path, err)
	}
	if device == nil || device.ID == nil {
		_ = container.Close()
		return nil, nil, fmt.Errorf("whatsapp: session at %s is not paired — run `forge channel whatsapp-login`", path)
	}
	return container, device, nil
}

// NewSessionDevice opens the store at path and returns a device to pair.
//
// An existing paired device is reused so a re-login refreshes the same session
// rather than accumulating rows for abandoned pairings. Exported for the
// `forge channel whatsapp-login` command, which owns the QR flow — the adapter
// itself never creates a device.
func NewSessionDevice(ctx context.Context, path string) (*sqlstore.Container, *store.Device, error) {
	container, err := openContainer(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, nil, fmt.Errorf("whatsapp: reading device from %s: %w", path, err)
	}
	if device == nil {
		device = container.NewDevice()
	}
	return container, device, nil
}

// SessionExists reports whether a paired session is present at path. Used by
// the CLI and the init wizard to decide whether to offer pairing.
func SessionExists(ctx context.Context, path string) bool {
	container, device, err := loadPairedDevice(ctx, path)
	if err != nil {
		return false
	}
	_ = container.Close()
	return device != nil && device.ID != nil
}
