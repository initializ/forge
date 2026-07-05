package compress

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/ctxzip/ccr"

	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/runtime"
)

func newRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := New(Config{StorePath: filepath.Join(t.TempDir(), "ctxzip.db")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// Regression: on a fresh project .forge/ does not exist yet — New must create
// the store's parent directory instead of failing open (found in live testing:
// "open .forge/ctxzip.db: no such file or directory").
func TestNew_CreatesMissingStoreDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".forge", "nested", "ctxzip.db")
	rt, err := New(Config{StorePath: path})
	if err != nil {
		t.Fatalf("New should create missing parent dirs: %v", err)
	}
	_ = rt.Close()
}

// bigJSON builds a large JSON-array tool output with one error row.
func bigJSON(n int) string {
	items := make([]map[string]any, n)
	for i := range items {
		items[i] = map[string]any{"name": fmt.Sprintf("pod-%03d", i), "status": "Running"}
	}
	items[n/2] = map[string]any{"name": "pod-bad", "status": "CrashLoopBackOff", "error": "OOMKilled"}
	b, _ := json.Marshal(items)
	return string(b)
}

func TestHook_CompressesToolOutput_AndExpandRoundTrips(t *testing.T) {
	rt := newRuntime(t)
	hook := rt.AfterToolExecHook()

	original := bigJSON(80)
	hctx := &runtime.HookContext{ToolName: "list_pods", ToolInput: `{"ns":"default"}`, ToolOutput: original}
	if err := hook(context.Background(), hctx); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if hctx.ToolOutput == original {
		t.Fatal("tool output was not compressed")
	}
	if !strings.Contains(hctx.ToolOutput, "CrashLoopBackOff") {
		t.Fatal("error row dropped — must-keep floor failed")
	}
	hashes := ccr.ExtractHashes(hctx.ToolOutput)
	if len(hashes) == 0 {
		t.Fatalf("no ctxzip marker in compressed output:\n%s", hctx.ToolOutput)
	}

	// The model recovers the original through the context_expand tool.
	tool := rt.ExpandTool()
	args, _ := json.Marshal(map[string]string{"hash": hashes[0]})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("context_expand: %v", err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) == 0 {
		t.Fatalf("expanded content invalid: err=%v rows=%d", err, len(rows))
	}
}

func TestHook_SkipsErrorsAndSmallOutput(t *testing.T) {
	rt := newRuntime(t)
	hook := rt.AfterToolExecHook()

	// Error results stay verbatim regardless of size.
	errOut := bigJSON(80)
	hctx := &runtime.HookContext{ToolName: "t", ToolOutput: errOut, Error: fmt.Errorf("boom")}
	_ = hook(context.Background(), hctx)
	if hctx.ToolOutput != errOut {
		t.Error("error output must not be compressed")
	}

	// Small outputs stay verbatim.
	small := &runtime.HookContext{ToolName: "t", ToolOutput: "tiny result"}
	_ = hook(context.Background(), small)
	if small.ToolOutput != "tiny result" {
		t.Error("small output must not be compressed")
	}
}

// Regression (live-test find): the loop chased its own tail — context_expand
// returned the original, the hook re-crushed it back into a marker. Expansion
// output must stay verbatim at both seams.
func TestNoRecompressionOfExpandOutput(t *testing.T) {
	rt := newRuntime(t)

	// Hook seam.
	big := bigJSON(80)
	hctx := &runtime.HookContext{ToolName: "context_expand", ToolOutput: big}
	_ = rt.AfterToolExecHook()(context.Background(), hctx)
	if hctx.ToolOutput != big {
		t.Fatal("hook recompressed context_expand output")
	}

	// Client-wrapper seam: an expansion result sitting in the live zone.
	inner := &capturingClient{}
	client := rt.WrapClient(inner)
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "count the pods"},
		{Role: llm.RoleTool, Name: "context_expand", ToolCallID: "tc9", Content: big},
		{Role: llm.RoleAssistant, Content: "counting"},
		{Role: llm.RoleUser, Content: "go on"},
	}
	_, _ = client.Chat(context.Background(), &llm.ChatRequest{Model: "m", Messages: msgs})
	if inner.lastReq.Messages[2].Content != big {
		t.Fatal("wrapper recompressed context_expand output in history")
	}
}

// capturingClient records the request it receives and returns a stub response.
type capturingClient struct {
	lastReq *llm.ChatRequest
}

func (c *capturingClient) Chat(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.lastReq = req
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: "ok"}}, nil
}

func (c *capturingClient) ChatStream(_ context.Context, req *llm.ChatRequest) (<-chan llm.StreamDelta, error) {
	c.lastReq = req
	ch := make(chan llm.StreamDelta)
	close(ch)
	return ch, nil
}

func (c *capturingClient) ModelID() string { return "test-model" }

func conversation(toolOutput string) []llm.ChatMessage {
	return []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: "You are a k8s assistant."},
		{Role: llm.RoleUser, Content: "check the pods"},
		{Role: llm.RoleTool, Name: "list_pods", ToolCallID: "tc1", Content: toolOutput},
		{Role: llm.RoleAssistant, Content: "Looking at the pods now."},
		{Role: llm.RoleUser, Content: "and the crashing one?"},
	}
}

func TestWrapClient_CompressesLiveZone_PreservesPrefixAndRecent(t *testing.T) {
	rt := newRuntime(t)
	inner := &capturingClient{}
	client := rt.WrapClient(inner)

	original := bigJSON(80)
	msgs := conversation(original)
	req := &llm.ChatRequest{Model: "m", Messages: msgs}

	if _, err := client.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	sent := inner.lastReq

	if sent.Messages[0].Content != msgs[0].Content {
		t.Error("frozen prefix (system) was modified")
	}
	if sent.Messages[3].Content != msgs[3].Content || sent.Messages[4].Content != msgs[4].Content {
		t.Error("protected recent turns were modified")
	}
	if sent.Messages[2].Content == original {
		t.Error("live-zone tool output was NOT compressed")
	}
	if sent.Messages[2].ToolCallID != "tc1" || sent.Messages[2].Name != "list_pods" {
		t.Error("tool message metadata lost in compression")
	}
	// Caller's request untouched.
	if req.Messages[2].Content != original {
		t.Error("caller's request was mutated")
	}
}

// Determinism is what keeps provider prompt caches hitting: the same
// conversation must compress to identical bytes on every call.
func TestWrapClient_Deterministic(t *testing.T) {
	rt := newRuntime(t)
	inner := &capturingClient{}
	client := rt.WrapClient(inner)

	original := bigJSON(80)
	req1 := &llm.ChatRequest{Model: "m", Messages: conversation(original)}
	_, _ = client.Chat(context.Background(), req1)
	first := inner.lastReq.Messages[2].Content

	// Same conversation, later turn appended (the tool message is deeper in
	// history now, and the latest user message differs).
	extended := append(conversation(original),
		llm.ChatMessage{Role: llm.RoleAssistant, Content: "It is OOMKilled."},
		llm.ChatMessage{Role: llm.RoleUser, Content: "what about the disk usage on node-a?"},
	)
	req2 := &llm.ChatRequest{Model: "m", Messages: extended}
	_, _ = client.Chat(context.Background(), req2)
	second := inner.lastReq.Messages[2].Content

	if first != second {
		t.Fatalf("compression not deterministic across turns — prompt cache would bust:\nfirst:  %.120s\nsecond: %.120s", first, second)
	}
}

func TestNormalizeHash(t *testing.T) {
	cases := map[string]string{
		"abc123def456":                        "abc123def456",
		"<<ctxzip:abc123 51_rows_offloaded>>": "abc123",
		"ctxzip:abc123":                       "abc123",
		"hash=abc123":                         "abc123",
		"  ABC123  ":                          "abc123",
		"ctxzip:abc123:108":                   "abc123", // count glued on (observed live)
	}
	for in, want := range cases {
		if got := normalizeHash(in); got != want {
			t.Errorf("normalizeHash(%q) = %q, want %q", in, got, want)
		}
	}
}

// Realized (wire) savings compound per RESEND: a tool output compressed once
// by the hook keeps saving its delta on every subsequent LLM call that
// carries it in history. This is the number invocation_complete reports —
// per-event accounting alone under-reported a live invocation 1,257 vs ~31K.
func TestWireSavings_CompoundPerResend(t *testing.T) {
	rt := newRuntime(t)
	ctx := runtime.WithCorrelationID(context.Background(), "task-wire")

	// Hook compresses one big tool output (creates a marker, saves S once).
	hctx := &runtime.HookContext{ToolName: "list_pods", ToolOutput: bigJSON(80)}
	_ = rt.AfterToolExecHook()(ctx, hctx)
	if !strings.Contains(hctx.ToolOutput, "<<ctxzip:") {
		t.Fatal("fixture did not compress")
	}

	// The compressed output now sits in history; three LLM calls resend it.
	inner := &capturingClient{}
	client := rt.WrapClient(inner)
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "check pods"},
		{Role: llm.RoleTool, Name: "list_pods", ToolCallID: "t1", Content: hctx.ToolOutput},
		{Role: llm.RoleAssistant, Content: "looking"},
		{Role: llm.RoleUser, Content: "go on"},
	}
	for i := 0; i < 3; i++ {
		_, _ = client.Chat(ctx, &llm.ChatRequest{Model: "m", Messages: msgs})
	}

	ct := rt.TakeInvocationTotals(ctx)
	if ct.SavedTokens <= 0 {
		t.Fatal("event savings missing")
	}
	// Three resends must credit ~3x the one-time event saving.
	if ct.WireSavedTokens < 2*ct.SavedTokens {
		t.Fatalf("wire savings did not compound: event=%d wire=%d (want wire ≈ 3x event)",
			ct.SavedTokens, ct.WireSavedTokens)
	}
}

// Compression must be attributable in the audit stream: every compression
// and expansion emits an event carrying per-event savings AND running totals,
// so auditors see cumulative savings, not just per-tool-call deltas.
func TestAuditEvents_SavingsAndTotals(t *testing.T) {
	type captured struct {
		event  string
		fields map[string]any
	}
	var events []captured

	rt, err := New(Config{
		StorePath: filepath.Join(t.TempDir(), "ctxzip.db"),
		Audit: func(_ context.Context, event string, fields map[string]any) {
			events = append(events, captured{event, fields})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	// Two hook compressions accumulate totals.
	hook := rt.AfterToolExecHook()
	h1 := &runtime.HookContext{ToolName: "list_pods", ToolOutput: bigJSON(80)}
	_ = hook(context.Background(), h1)
	h2 := &runtime.HookContext{ToolName: "list_nodes", ToolOutput: bigJSON(90)}
	_ = hook(context.Background(), h2)

	if len(events) != 2 {
		t.Fatalf("want 2 context_compressed events, got %d", len(events))
	}
	for i, e := range events {
		if e.event != AuditEventCompressed {
			t.Fatalf("event %d = %s, want %s", i, e.event, AuditEventCompressed)
		}
		if e.fields["seam"] != "tool_output" {
			t.Errorf("event %d seam = %v", i, e.fields["seam"])
		}
		if saved, _ := e.fields["saved_tokens"].(int); saved <= 0 {
			t.Errorf("event %d saved_tokens = %v, want > 0", i, e.fields["saved_tokens"])
		}
	}
	// Running total on the second event exceeds its own per-event saving.
	second := events[1].fields
	if second["total_compressions"].(int64) != 2 {
		t.Errorf("total_compressions = %v, want 2", second["total_compressions"])
	}
	if second["total_saved_tokens"].(int64) <= int64(second["saved_tokens"].(int)) {
		t.Errorf("running total %v should exceed per-event %v",
			second["total_saved_tokens"], second["saved_tokens"])
	}

	// An expansion emits the cost-side event with the same totals context.
	hashes := ccr.ExtractHashes(h1.ToolOutput)
	args, _ := json.Marshal(map[string]string{"hash": hashes[0]})
	_, _ = rt.ExpandTool().Execute(context.Background(), args)

	last := events[len(events)-1]
	if last.event != AuditEventExpanded {
		t.Fatalf("want %s event after expand, got %s", AuditEventExpanded, last.event)
	}
	if last.fields["hit"] != true || last.fields["total_expansions"].(int64) != 1 {
		t.Errorf("expansion fields wrong: %+v", last.fields)
	}

	// Totals snapshot agrees.
	tot := rt.Totals()
	if tot.Compressions != 2 || tot.Expansions != 1 || tot.SavedTokens <= 0 {
		t.Errorf("Totals() = %+v", tot)
	}
}

// The runtime-owned system directive is what makes compression work for ANY
// skill without skill authors documenting it. Guard that it names the real
// tool and the real marker prefix, so a rename cannot silently orphan it.
func TestSystemDirective_MatchesToolAndMarker(t *testing.T) {
	if !strings.Contains(SystemDirective, expandToolName) {
		t.Errorf("SystemDirective does not mention the %s tool", expandToolName)
	}
	if !strings.Contains(SystemDirective, ccr.MarkerPrefix) {
		t.Errorf("SystemDirective does not show the %q marker shape", ccr.MarkerPrefix)
	}
}

// Per-invocation savings are keyed by correlation ID so concurrent tasks
// don't cross-contaminate, and TakeInvocationTotals pops exactly once.
func TestTakeInvocationTotals_PerCorrelation(t *testing.T) {
	rt := newRuntime(t)
	hook := rt.AfterToolExecHook()

	ctxA := runtime.WithCorrelationID(context.Background(), "task-a")
	ctxB := runtime.WithCorrelationID(context.Background(), "task-b")

	// Two compressions under A, one under B.
	for _, c := range []struct {
		ctx context.Context
		n   int
	}{{ctxA, 80}, {ctxA, 90}, {ctxB, 100}} {
		h := &runtime.HookContext{ToolName: "t", ToolOutput: bigJSON(c.n)}
		_ = hook(c.ctx, h)
	}

	a := rt.TakeInvocationTotals(ctxA)
	if a.Compressions != 2 || a.SavedTokens <= 0 {
		t.Fatalf("A totals = %+v, want 2 compressions", a)
	}
	b := rt.TakeInvocationTotals(ctxB)
	if b.Compressions != 1 {
		t.Fatalf("B totals = %+v, want 1 compression", b)
	}
	// Popped — a second take returns zeros.
	if again := rt.TakeInvocationTotals(ctxA); again.Compressions != 0 {
		t.Fatalf("second take should be empty, got %+v", again)
	}
	// No correlation ID → zeros, no accumulation.
	if none := rt.TakeInvocationTotals(context.Background()); none.Compressions != 0 {
		t.Fatalf("no-correlation take should be empty, got %+v", none)
	}
}

// PR #241 review: perInvocation must be bounded — an emission path that
// misses TakeInvocationTotals must not leak buckets forever. Oldest-touched
// buckets are evicted at the cap; the newest survives.
func TestPerInvocationBuckets_Bounded(t *testing.T) {
	rt := newRuntime(t)
	for i := 0; i < maxInvocationBuckets+50; i++ {
		ctx := runtime.WithCorrelationID(context.Background(), fmt.Sprintf("task-%05d", i))
		rt.recordCompression(ctx, "tool_output", "t", 100, 10)
	}
	rt.mu.Lock()
	n := len(rt.perInvocation)
	rt.mu.Unlock()
	if n > maxInvocationBuckets {
		t.Fatalf("perInvocation grew past cap: %d > %d", n, maxInvocationBuckets)
	}
	// The most recent bucket must still be intact.
	last := runtime.WithCorrelationID(context.Background(), fmt.Sprintf("task-%05d", maxInvocationBuckets+49))
	if got := rt.TakeInvocationTotals(last); got.Compressions != 1 {
		t.Fatalf("newest bucket evicted: %+v", got)
	}
}

// PR #241 review: the recent-marker set must be bounded, evicting
// oldest-emitted hashes first (only recent markers matter for repairing
// imperfect transcriptions; exact hashes still resolve via the store).
func TestRecentMarkers_Bounded(t *testing.T) {
	rt := newRuntime(t)
	// Fixed-width distinct hashes: "mk<6 digits>xx" — no hash is a prefix of
	// another, so resolvePrefix answers are about presence, not ambiguity.
	hash := func(i int) string { return fmt.Sprintf("mk%06dxx", i) }
	for i := 0; i < maxRecentMarkers+10; i++ {
		rt.rememberMarkers([]string{hash(i)}, 10)
	}
	rt.mu.Lock()
	n, order := len(rt.recent), len(rt.recentOrder)
	rt.mu.Unlock()
	if n > maxRecentMarkers || order > maxRecentMarkers {
		t.Fatalf("recent markers grew past cap: map=%d order=%d", n, order)
	}
	// Oldest evicted, newest still resolvable.
	if got := rt.resolvePrefix(hash(0)); got != "" {
		t.Errorf("oldest marker should have been evicted, resolved %q", got)
	}
	if got := rt.resolvePrefix(hash(maxRecentMarkers + 9)); got != hash(maxRecentMarkers+9) {
		t.Errorf("newest marker should resolve, got %q", got)
	}
}

// KeepPatterns (forge.yaml compression.keep_patterns) must flow through to
// the hook so builder-flagged rows survive the compressed view.
func TestHook_KeepPatterns(t *testing.T) {
	rt, err := New(Config{
		StorePath:    filepath.Join(t.TempDir(), "ctxzip.db"),
		KeepPatterns: []string{"Quarantined"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	items := make([]map[string]any, 80)
	for i := range items {
		items[i] = map[string]any{"id": fmt.Sprintf("r-%03d", i), "state": "nominal", "zone": "us-east-1"}
	}
	items[40] = map[string]any{"id": "r-040", "state": "QUARANTINED", "zone": "us-east-1"}
	blob, _ := json.Marshal(items)

	hctx := &runtime.HookContext{ToolName: "fleet_list", ToolOutput: string(blob)}
	_ = rt.AfterToolExecHook()(context.Background(), hctx)
	if hctx.ToolOutput == string(blob) {
		t.Fatal("expected compression to occur")
	}
	if !strings.Contains(hctx.ToolOutput, "QUARANTINED") {
		t.Fatal("keep_patterns row dropped from compressed view")
	}
}

// Regression (live-test find): models truncate hex hashes when transcribing a
// marker into a tool call. A unique prefix of an emitted hash must resolve.
func TestExpandTool_ResolvesTruncatedHash(t *testing.T) {
	rt := newRuntime(t)
	hook := rt.AfterToolExecHook()
	hctx := &runtime.HookContext{ToolName: "list_pods", ToolOutput: bigJSON(80)}
	_ = hook(context.Background(), hctx)

	hashes := ccr.ExtractHashes(hctx.ToolOutput)
	if len(hashes) == 0 {
		t.Fatal("no marker emitted")
	}
	// The model passes only the first 8 chars of the hash.
	args, _ := json.Marshal(map[string]string{"hash": hashes[0][:8]})
	out, err := rt.ExpandTool().Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "No stored content") {
		t.Fatalf("truncated hash did not resolve via prefix: %q", out[:80])
	}
}

func TestExpandTool_MissIsHelpful(t *testing.T) {
	rt := newRuntime(t)
	args, _ := json.Marshal(map[string]string{"hash": "deadbeefdeadbeefdeadbeef"})
	out, err := rt.ExpandTool().Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("miss should not be an error: %v", err)
	}
	if !strings.Contains(out, "Re-run") {
		t.Errorf("miss message should guide regeneration: %q", out)
	}
}
