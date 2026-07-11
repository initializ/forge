package compress

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/initializ/ctxzip/ccr"
	"github.com/initializ/forge/forge-core/runtime"
)

func TestExtractCandidates(t *testing.T) {
	content := `pod status SchedulingGated, node has DiskPressure, DiskPressure again,
also QUOTA_EXCEEDED and plain words and ImagePullBackOff and a hexvalue deadbeef`

	got := extractCandidates(content, []string{"quota_exceeded"})

	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "DiskPressure") || !strings.Contains(joined, "SchedulingGated") {
		t.Fatalf("expected domain-state tokens, got %v", got)
	}
	// ImagePullBackOff contains "backoff" — on the built-in error floor, so
	// it was KEPT and cannot be why the model expanded.
	if strings.Contains(joined, "ImagePullBackOff") {
		t.Fatalf("floor-kept token must be excluded: %v", got)
	}
	// QUOTA_EXCEEDED is already in keep_patterns.
	if strings.Contains(joined, "QUOTA_EXCEEDED") {
		t.Fatalf("configured keep pattern must be excluded: %v", got)
	}
	// Most frequent first: DiskPressure appears twice.
	if len(got) == 0 || got[0] != "DiskPressure" {
		t.Fatalf("frequency ordering broken: %v", got)
	}
}

func TestSuggestionStore_ThresholdAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), SuggestionsFileName)
	s := newSuggestionStore(path)
	now := time.Unix(1000, 0)

	// Two expansions: below threshold, nothing crossed.
	if crossed := s.record("kubectl", []string{"DiskPressure"}, now); len(crossed) != 0 {
		t.Fatalf("crossed too early: %v", crossed)
	}
	if crossed := s.record("kubectl", []string{"DiskPressure"}, now); len(crossed) != 0 {
		t.Fatalf("crossed too early: %v", crossed)
	}
	// Third expansion crosses — surfaced exactly once.
	crossed := s.record("helm", []string{"DiskPressure"}, now)
	if len(crossed) != 1 || crossed[0].Pattern != "DiskPressure" || crossed[0].Expansions != 3 {
		t.Fatalf("threshold crossing wrong: %+v", crossed)
	}
	if len(crossed[0].Tools) != 2 {
		t.Fatalf("tools not accumulated: %v", crossed[0].Tools)
	}
	if again := s.record("kubectl", []string{"DiskPressure"}, now); len(again) != 0 {
		t.Fatalf("suggestion surfaced twice: %v", again)
	}

	// Persistence: a fresh store sees the state.
	s2 := newSuggestionStore(path)
	snap := s2.Snapshot()
	if len(snap) != 1 || !snap[0].Suggested || snap[0].Expansions != 4 {
		t.Fatalf("persistence broken: %+v", snap)
	}
}

func TestSuggestionStore_Bounded(t *testing.T) {
	s := newSuggestionStore(filepath.Join(t.TempDir(), SuggestionsFileName))
	now := time.Unix(1000, 0)
	for i := 0; i < maxTrackedPatterns+30; i++ {
		s.record("t", []string{fmt.Sprintf("TokenNumber%dX", i)}, now.Add(time.Duration(i)*time.Second))
	}
	if n := len(s.Snapshot()); n > maxTrackedPatterns {
		t.Fatalf("store grew past cap: %d", n)
	}
}

// End-to-end: expansions through the context_expand tool feed the flywheel,
// and crossing the threshold emits context_pattern_suggested exactly once.
func TestFlywheel_EndToEnd(t *testing.T) {
	var events []string
	var expandedCandidates [][]string
	rt, err := New(Config{
		StorePath: filepath.Join(t.TempDir(), "ctxzip.db"),
		Audit: func(_ context.Context, event string, fields map[string]any) {
			switch event {
			case AuditEventPatternSuggested:
				events = append(events, fmt.Sprintf("%s:%v", fields["pattern"], fields["expansions"]))
			case AuditEventExpanded:
				if c, ok := fields["candidates"].([]string); ok {
					expandedCandidates = append(expandedCandidates, c)
				}
				if tool, _ := fields["tool"].(string); tool != "list_nodes" {
					t.Errorf("context_expanded missing producing tool, got %v", fields["tool"])
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	hook := rt.AfterToolExecHook()
	tool := rt.ExpandTool()
	ctx := runtime.WithCorrelationID(context.Background(), "fw-task")

	// Three rounds: compress content whose DROPPED middle contains a
	// domain-state token the floor does not know, then expand it.
	for round := 0; round < 3; round++ {
		items := make([]map[string]any, 60)
		for i := range items {
			items[i] = map[string]any{"id": fmt.Sprintf("r%d-%03d", round, i), "state": "nominal", "zone": "us-east-1"}
		}
		items[30] = map[string]any{"id": fmt.Sprintf("r%d-030", round), "state": "NodeAffinityMismatch", "zone": "us-east-1"}
		blob, _ := json.Marshal(items)

		hctx := &runtime.HookContext{ToolName: "list_nodes", ToolOutput: string(blob)}
		if err := hook(ctx, hctx); err != nil {
			t.Fatal(err)
		}
		hashes := ccr.ExtractHashes(hctx.ToolOutput)
		if len(hashes) != 1 {
			t.Fatalf("round %d: no marker", round)
		}
		args, _ := json.Marshal(map[string]string{"hash": hashes[0]})
		if _, err := tool.Execute(ctx, args); err != nil {
			t.Fatal(err)
		}
	}

	if len(events) != 1 || !strings.HasPrefix(events[0], "NodeAffinityMismatch:") {
		t.Fatalf("want exactly one NodeAffinityMismatch suggestion event, got %v", events)
	}
	// The CLI-facing snapshot shows it as suggested.
	found := false
	for _, st := range rt.Suggestions() {
		if st.Pattern == "NodeAffinityMismatch" && st.Suggested {
			found = true
		}
	}
	if !found {
		t.Fatalf("suggestion missing from snapshot: %+v", rt.Suggestions())
	}

	// Every context_expanded event carried the mined candidates — the
	// restart-immune, fleet-aggregatable channel (a platform can rebuild
	// counting from the audit stream alone).
	if len(expandedCandidates) != 3 {
		t.Fatalf("want candidates on all 3 expansion events, got %d", len(expandedCandidates))
	}
	for i, c := range expandedCandidates {
		if len(c) == 0 || len(c) > maxEventCandidates {
			t.Fatalf("event %d candidates out of bounds: %v", i, c)
		}
		if !containsStr(c, "NodeAffinityMismatch") {
			t.Fatalf("event %d candidates missing the domain token: %v", i, c)
		}
	}
}
