package optimizer

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestClusterByEntities_GroupsSharedFiles(t *testing.T) {
	eps := []Episode{
		{ID: "e1", Entities: []string{"client.go"}},
		{ID: "e2", Entities: []string{"client.go", "client_test.go"}},
		{ID: "e3", Entities: []string{"client_test.go"}},
		{ID: "e4", Entities: []string{"unrelated.go"}}, // singleton → excluded
	}
	clusters := clusterByEntities(eps, 3)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster of >=3, got %d", len(clusters))
	}
	if len(clusters[0]) != 3 {
		t.Fatalf("cluster size = %d, want 3 (e1,e2,e3 linked via shared files)", len(clusters[0]))
	}
}

func TestConsolidateRepo_WritesProcedureFromCluster(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"e1", "e2", "e3"} {
		if err := store.WriteEpisode(Episode{
			ID: id, Kind: KindEpisodic, Repo: "forge",
			TaskSignature: "work on client " + id, Outcome: OutcomeSuccess,
			Entities: []string{"client.go"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	fd := &fakeDistiller{result: &Episode{}}
	former := newFormer(t, MemoryFormerConfig{
		Store: store, Distiller: fd, Repo: "forge",
		Consolidate: true, ConsolidateEvery: 3, MinCluster: 3,
	})

	former.consolidateRepo(DistillInput{Repo: "forge", Model: "m", Upstream: "http://x", Headers: http.Header{}})

	eps, _ := store.ListEpisodes("forge", 0)
	var procs []Episode
	for _, e := range eps {
		if e.Kind == KindProcedural {
			procs = append(procs, e)
		}
	}
	if len(procs) != 1 {
		t.Fatalf("expected 1 procedure, got %d", len(procs))
	}
	p := procs[0]
	if len(p.EpisodeIDs) != 3 {
		t.Errorf("procedure evidence = %v, want 3 episode ids", p.EpisodeIDs)
	}
	if p.About != "resource:repo:forge" || p.SourceKind != "consolidation" {
		t.Errorf("procedure not stamped: about=%q source=%q", p.About, p.SourceKind)
	}
	if fd.procCalls == nil || len(fd.procCalls) != 1 {
		t.Errorf("expected 1 DistillProcedure call, got %d", len(fd.procCalls))
	}

	// Idempotent: re-running with the same episodes must NOT create a duplicate.
	former.consolidateRepo(DistillInput{Repo: "forge", Model: "m", Upstream: "http://x", Headers: http.Header{}})
	eps, _ = store.ListEpisodes("forge", 0)
	got := 0
	for _, e := range eps {
		if e.Kind == KindProcedural {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("re-consolidation created duplicates: %d procedures", got)
	}
}

func TestRecallScore_ProceduresRankAboveEpisodes(t *testing.T) {
	proc := Episode{Kind: KindProcedural}
	epi := Episode{Kind: KindEpisodic, Lesson: "x", Outcome: OutcomeSuccess}
	if recallScore(proc) <= recallScore(epi) {
		t.Fatalf("procedure score %d should exceed episode score %d", recallScore(proc), recallScore(epi))
	}
}

func TestParseProcedureJSON_MapsFields(t *testing.T) {
	// The exact shape the consolidation prompt asks for.
	raw := "```json\n{\"title\":\"add a new optimizer subcommand\",\"description\":\"When adding a subcommand, wire command+flags+cleanup.\",\"steps\":[\"define cobra command\",\"register flags\"],\"pitfalls\":[\"forgetting to close the store\"],\"lesson\":\"keep flag defaults and cleanup symmetric\"}\n```"
	p, err := parseProcedureJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindProcedural {
		t.Errorf("kind = %q, want procedural", p.Kind)
	}
	if p.TaskSignature != "add a new optimizer subcommand" {
		t.Errorf("title→task_signature = %q", p.TaskSignature)
	}
	if len(p.Actions) != 2 { // steps → actions
		t.Errorf("steps→actions = %v", p.Actions)
	}
	if len(p.Errors) != 1 { // pitfalls → errors
		t.Errorf("pitfalls→errors = %v", p.Errors)
	}
	if p.Lesson == "" {
		t.Errorf("lesson empty")
	}
}
