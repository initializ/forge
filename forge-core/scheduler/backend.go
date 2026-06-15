package scheduler

import "context"

// Backend abstracts the persistence + timing layer the runner uses for
// scheduled tasks. Two implementations ship today (#162):
//
//   - FileBackend: wraps the existing Scheduler ticker + MemoryScheduleStore.
//     Persistence is a markdown file at <WorkDir>/.forge/memory/SCHEDULES.md;
//     timing is a 30s goroutine ticker; overlap is prevented by an
//     in-process map of "currently running" flags.
//
//   - KubernetesBackend (forge-cli/runtime/scheduler_k8s.go): persists
//     schedules as K8s CronJob resources via client-go. Timing is the
//     cluster's CronJob controller. Overlap is prevented by
//     CronJob.Spec.ConcurrencyPolicy=Forbid (K8s's native equivalent of
//     the file backend's running map).
//
// The Backend interface intentionally bundles "timing concerns" (Start,
// Stop, Reload) with "persistence concerns" (List, Get, Set, Delete,
// Sync) into one surface because the two are co-located in the file
// backend's existing implementation and entirely owned by the cluster
// in the kubernetes backend. Splitting them would force one or the
// other backend to implement no-op methods.
type Backend interface {
	// Start launches any backend-specific goroutines (file backend: the
	// 30s ticker). For backends that delegate timing to an external
	// system (kubernetes backend: the CronJob controller), Start is a
	// no-op. Must be safe to call once per backend instance.
	Start(ctx context.Context)

	// Stop signals the backend to terminate any goroutines launched by
	// Start and waits for them to exit. Idempotent.
	Stop()

	// Reload re-reads any cached state (file backend: the parsed-cron
	// cache). For backends with no cached state (kubernetes backend:
	// each operation hits the API), Reload is a no-op.
	Reload(ctx context.Context)

	// Sync reconciles the backend's state with the declarative list
	// of schedules pulled from forge.yaml. Called once at startup
	// after Start and again on hot-reload. Existing schedules with
	// matching IDs are updated in-place; new ones are added;
	// previously-yaml-sourced schedules no longer in the list are
	// deleted (LLM-sourced schedules are left alone — they're owned
	// by the agent's chat history, not the declarative manifest).
	Sync(ctx context.Context, declared []Schedule) error

	// List returns every active schedule the backend knows about.
	List(ctx context.Context) ([]Schedule, error)

	// Get returns a single schedule by ID, or nil when absent.
	Get(ctx context.Context, id string) (*Schedule, error)

	// Set creates or updates a schedule. Schedule.Source distinguishes
	// declarative (forge.yaml) entries from LLM-set ones; backends use
	// it to enforce RBAC in the kubernetes case and labeling in both.
	Set(ctx context.Context, sched Schedule) error

	// Delete removes a schedule by ID. Returns nil when the schedule
	// did not exist (idempotent).
	Delete(ctx context.Context, id string) error

	// History returns recent run records for a schedule (or all
	// schedules when scheduleID is empty). File backend reads from
	// the SCHEDULES.md history block; kubernetes backend returns
	// empty + a logger.Warn deferring to the audit stream (which
	// already carries schedule_complete events with status +
	// duration).
	History(ctx context.Context, scheduleID string, limit int) ([]HistoryEntry, error)
}

// FileBackend is the default Backend implementation: wraps the
// existing Scheduler tick loop and ScheduleStore behind the unified
// Backend interface. Zero behavior change vs the pre-#162 wiring —
// constructed via NewFileBackend at the runner's existing scheduler
// init site. The wrapping is structural (delegates everything to the
// underlying Scheduler / Store), not a reimplementation.
type FileBackend struct {
	store ScheduleStore
	sched *Scheduler
}

// NewFileBackend constructs a FileBackend wrapping the given store +
// scheduler. The caller still owns store + scheduler lifecycle outside
// of Start/Stop on the Backend.
func NewFileBackend(store ScheduleStore, sched *Scheduler) *FileBackend {
	return &FileBackend{store: store, sched: sched}
}

func (b *FileBackend) Start(ctx context.Context)  { b.sched.Start(ctx) }
func (b *FileBackend) Stop()                      { b.sched.Stop() }
func (b *FileBackend) Reload(ctx context.Context) { b.sched.Reload(ctx) }

// Sync upserts declared (yaml-sourced) schedules into the store and
// removes any pre-existing yaml-sourced schedules that are no longer
// in the declared list. LLM-sourced schedules are preserved.
func (b *FileBackend) Sync(ctx context.Context, declared []Schedule) error {
	existing, err := b.store.List(ctx)
	if err != nil {
		return err
	}
	declaredIDs := make(map[string]bool, len(declared))
	for _, d := range declared {
		declaredIDs[d.ID] = true
		// Preserve existing per-run state when updating.
		if cur, _ := b.store.Get(ctx, d.ID); cur != nil {
			d.LastRun = cur.LastRun
			d.LastStatus = cur.LastStatus
			d.RunCount = cur.RunCount
			if cur.Created.IsZero() {
				// Should not happen in practice but stays safe.
			} else {
				d.Created = cur.Created
			}
		}
		if err := b.store.Set(ctx, d); err != nil {
			return err
		}
	}
	// Delete previously-yaml-sourced entries no longer in the manifest.
	for _, e := range existing {
		if e.Source != SourceYAML {
			continue
		}
		if declaredIDs[e.ID] {
			continue
		}
		if err := b.store.Delete(ctx, e.ID); err != nil {
			return err
		}
	}
	b.sched.Reload(ctx)
	return nil
}

func (b *FileBackend) List(ctx context.Context) ([]Schedule, error) {
	return b.store.List(ctx)
}

func (b *FileBackend) Get(ctx context.Context, id string) (*Schedule, error) {
	return b.store.Get(ctx, id)
}

func (b *FileBackend) Set(ctx context.Context, sched Schedule) error {
	if err := b.store.Set(ctx, sched); err != nil {
		return err
	}
	b.sched.Reload(ctx)
	return nil
}

func (b *FileBackend) Delete(ctx context.Context, id string) error {
	if err := b.store.Delete(ctx, id); err != nil {
		return err
	}
	b.sched.Reload(ctx)
	return nil
}

func (b *FileBackend) History(ctx context.Context, scheduleID string, limit int) ([]HistoryEntry, error) {
	return b.store.History(ctx, scheduleID, limit)
}

// Store exposes the underlying ScheduleStore for callers (the schedule_*
// builtin tools registered by the runner) that already speak the
// ScheduleStore vocabulary. KubernetesBackend exposes a thin adapter
// over its CronJob CRUD here so the builtin tools work in both modes
// without per-tool branching.
func (b *FileBackend) Store() ScheduleStore { return b.store }

// Source constants mark schedules by origin so Sync can reconcile
// declarative state without nuking LLM-set entries.
const (
	// SourceYAML marks schedules synced in from forge.yaml's
	// `schedules[]` block at startup or hot-reload.
	SourceYAML = "yaml"
	// SourceLLM marks schedules created at runtime by the LLM via the
	// schedule_set builtin tool.
	SourceLLM = "llm"
)
