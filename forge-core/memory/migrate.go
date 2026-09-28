package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// migratedSentinel is the marker written into a repo-local memory directory
// once its contents have been copied into the global namespace. Its presence
// makes migration idempotent and records where the copy went.
const migratedSentinel = ".migrated"

// dailyLogRe matches the legacy daily-log filenames (YYYY-MM-DD.md) that the
// repo-local store wrote; these become episodic sessions under the global
// namespace.
var dailyLogRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.md$`)

// MigrateResult reports what a migration did, or would do for a dry run.
type MigrateResult struct {
	AgentID  string   `json:"agent_id"`
	Source   string   `json:"source"`
	Dest     string   `json:"dest"`
	Files    []string `json:"files"` // destination-relative paths (would be) copied
	Skipped  bool     `json:"skipped"`
	DryRun   bool     `json:"dry_run"`
	SkipNote string   `json:"skip_note,omitempty"`
}

// sentinelData is the JSON payload written into the .migrated sentinel.
type sentinelData struct {
	MigratedAt time.Time `json:"migrated_at"`
	Dest       string    `json:"dest"`
}

// MigrateRepoLocal copies a repo-local .forge/memory directory (sourceDir) into
// the global agents namespace at <root>/agents/<agentID>/. Daily logs
// (YYYY-MM-DD.md) are placed under sessions/; MEMORY.md, index/, and any other
// .md files are copied at their relative position. The source is left intact —
// migration is a copy, not a move — and a .migrated sentinel recording the
// destination is written so a re-run is a no-op.
//
// When dryRun is true nothing is written; the returned result lists the files
// that would be copied.
func MigrateRepoLocal(root, agentID, sourceDir string, dryRun bool) (*MigrateResult, error) {
	safeAgent := AgentID(agentID)
	dest := filepath.Join(root, NamespaceAgents, safeAgent)

	res := &MigrateResult{
		AgentID: safeAgent,
		Source:  sourceDir,
		Dest:    dest,
		DryRun:  dryRun,
	}

	info, err := os.Stat(sourceDir)
	if err != nil || !info.IsDir() {
		res.Skipped = true
		res.SkipNote = "no repo-local memory directory"
		return res, nil
	}

	// Already migrated? The sentinel makes this idempotent.
	if _, err := os.Stat(filepath.Join(sourceDir, migratedSentinel)); err == nil {
		res.Skipped = true
		res.SkipNote = "already migrated (.migrated sentinel present)"
		return res, nil
	}

	// Plan the copy: map each source file to its destination-relative path.
	type copyOp struct{ src, relDest string }
	var ops []copyOp
	err = filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == migratedSentinel {
			return nil
		}
		relDest := rel
		// Daily logs at the top level become episodic sessions.
		if filepath.Dir(rel) == "." && dailyLogRe.MatchString(filepath.Base(rel)) {
			relDest = filepath.Join("sessions", filepath.Base(rel))
		}
		ops = append(ops, copyOp{src: path, relDest: relDest})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning source: %w", err)
	}

	sort.Slice(ops, func(i, j int) bool { return ops[i].relDest < ops[j].relDest })
	for _, op := range ops {
		res.Files = append(res.Files, op.relDest)
	}

	if dryRun {
		return res, nil
	}

	for _, op := range ops {
		data, err := os.ReadFile(op.src)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", op.src, err)
		}
		if err := atomicWriteFile(filepath.Join(dest, op.relDest), data, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", op.relDest, err)
		}
	}

	// Record the migration in both the registry and a source-side sentinel.
	reg, err := OpenRegistry(root, NamespaceAgents)
	if err != nil {
		return nil, err
	}
	if err := reg.Upsert(RegistryEntry{
		ID:         safeAgent,
		Source:     agentID,
		Name:       agentID,
		LocalPaths: []string{sourceDir},
	}); err != nil {
		return nil, fmt.Errorf("updating registry: %w", err)
	}

	sentinel, err := json.MarshalIndent(sentinelData{MigratedAt: time.Now().UTC(), Dest: dest}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicWriteFile(filepath.Join(sourceDir, migratedSentinel), sentinel, 0o644); err != nil {
		return nil, fmt.Errorf("writing sentinel: %w", err)
	}

	return res, nil
}
