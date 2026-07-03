// Package compress wires reversible context compression (ctxzip) into the
// forge agent loop.
//
// ctxzip shrinks bulky content before it reaches the LLM — tool outputs,
// logs, JSON — and offloads everything it drops to a durable local store,
// replaced inline by a retrievable "<<ctxzip:HASH ...>>" marker. Compression
// is lossy on the wire but lossless end-to-end: the model can recover any
// original via the context_expand tool.
//
// Three integration seams, all owned by Runtime:
//
//   - AfterToolExecHook — compresses tool output once, at production time,
//     before it enters Memory. Because the compressed bytes never change
//     afterwards, the conversation prefix stays byte-stable across turns and
//     provider prompt caches keep hitting.
//   - WrapClient — an llm.Client decorator that compresses the live zone of
//     every outbound request (skipping the frozen prefix and recent turns).
//     It is deliberately deterministic: the relevance query is pinned to the
//     first user message of the session, never the latest turn, so the same
//     historic message always compresses to the same bytes.
//   - ExpandTool — the context_expand builtin that retrieves originals from
//     the store by marker hash.
//
// The store is a bbolt file (default .forge/ctxzip.db) so originals survive
// process restarts; entries expire after TTL, at which point the disk or the
// original command is the source of truth (the tool's miss message says so).
package compress

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/initializ/ctxzip/ccr"

	"github.com/initializ/forge/forge-core/runtime"
)

// Defaults for Config fields left at their zero value.
const (
	DefaultTTL                = 30 * time.Minute
	DefaultMinToolOutputChars = 2048
)

// Config configures the compression runtime.
type Config struct {
	// StorePath is the bbolt file for the CCR store (required),
	// e.g. .forge/ctxzip.db.
	StorePath string
	// TTL is how long offloaded originals stay retrievable. Default 30m.
	TTL time.Duration
	// MinToolOutputChars is the size below which tool outputs are left
	// alone by the AfterToolExec hook. Default 2048.
	MinToolOutputChars int
	// Logger is optional; nil disables logging.
	Logger runtime.Logger
}

// Runtime owns the shared CCR store and produces the hook, client wrapper,
// and expand tool that plug into the agent loop.
type Runtime struct {
	store   *ccr.BoltStore
	minSize int
	logger  runtime.Logger

	// recent remembers marker hashes this process emitted so the expand tool
	// can resolve a unique prefix when the model transcribes a hash
	// imperfectly (observed live: models truncate hex hashes).
	mu     sync.Mutex
	recent map[string]struct{}
}

// rememberMarkers records emitted marker hashes for prefix resolution.
func (r *Runtime) rememberMarkers(hashes []string) {
	if len(hashes) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recent == nil {
		r.recent = make(map[string]struct{})
	}
	for _, h := range hashes {
		r.recent[h] = struct{}{}
	}
}

// resolvePrefix returns the unique remembered hash starting with prefix, or
// "" when the prefix is too short, unknown, or ambiguous.
func (r *Runtime) resolvePrefix(prefix string) string {
	if len(prefix) < 6 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var found string
	for h := range r.recent {
		if strings.HasPrefix(h, prefix) {
			if found != "" {
				return "" // ambiguous
			}
			found = h
		}
	}
	return found
}

// New opens the durable store and returns a Runtime. Call Close on shutdown.
func New(cfg Config) (*Runtime, error) {
	if cfg.StorePath == "" {
		return nil, fmt.Errorf("compress: Config.StorePath is required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.MinToolOutputChars <= 0 {
		cfg.MinToolOutputChars = DefaultMinToolOutputChars
	}
	// bbolt creates the DB file but not its parent directory; on a fresh
	// project .forge/ does not exist yet, so create it (0700 — originals can
	// hold sensitive tool output).
	if dir := filepath.Dir(cfg.StorePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("compress: creating store dir: %w", err)
		}
	}
	store, err := ccr.NewBoltStore(ccr.BoltConfig{Path: cfg.StorePath, TTL: cfg.TTL})
	if err != nil {
		return nil, fmt.Errorf("compress: opening store: %w", err)
	}
	return &Runtime{store: store, minSize: cfg.MinToolOutputChars, logger: cfg.Logger}, nil
}

// Close releases the underlying store.
func (r *Runtime) Close() error {
	return r.store.Close()
}

// Store exposes the CCR store (used by tests and diagnostics).
func (r *Runtime) Store() ccr.Store { return r.store }

func (r *Runtime) debugf(msg string, fields map[string]any) {
	if r.logger != nil {
		r.logger.Debug(msg, fields)
	}
}
