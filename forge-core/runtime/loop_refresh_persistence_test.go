package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
)

// Regression test for the "browser refresh mid-response" bug: a client
// disconnect (e.g. a page reload) cancels the context passed to Execute
// before the LLM call ever completes. Before the fix, persistSession was
// only called on the successful-completion paths, so the user's own
// message — and any tool-call progress already made — was never written
// to disk if that cancellation raced ahead of completion. This test
// drives Execute with a context that's cancelled synchronously from
// inside the (mocked) LLM call, using a REAL file-backed MemoryStore, and
// asserts the persisted session file on disk contains the user's message
// even though Execute returns context.Canceled.
func TestLLMExecutor_RefreshMidResponse_UserMessagePersisted(t *testing.T) {
	store, err := NewMemoryStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	var cancel context.CancelFunc
	chatCalls := 0

	exec := NewLLMExecutor(LLMExecutorConfig{
		Client: &mockLLMClient{
			chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
				chatCalls++
				// Simulate the browser tab reloading right as the agent
				// starts processing the turn: the client disconnects,
				// forge-ui's proxy context cancels, which (pre-fix) would
				// propagate all the way here before any persist happened.
				cancel()
				return nil, context.Canceled
			},
		},
		Tools:     &mockToolExecutor{toolDefs: []llm.ToolDefinition{}},
		Store:     store,
		ModelName: "test",
		Provider:  "test",
	})

	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	const taskID = "refresh-test-task"
	_, err = exec.Execute(ctx,
		&a2a.Task{ID: taskID},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("hello, are you there?")}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute should return context.Canceled (simulating the refresh-aborted turn), got %v", err)
	}
	if chatCalls != 1 {
		t.Fatalf("expected exactly 1 LLM call before cancellation, got %d", chatCalls)
	}

	// The critical assertion: even though the turn never completed, the
	// user's message must already be on disk — durability of input must
	// not depend on the response finishing.
	saved, loadErr := store.Load(taskID)
	if loadErr != nil {
		t.Fatalf("store.Load: %v", loadErr)
	}
	if saved == nil {
		t.Fatal("session was never persisted — the user's message is lost on refresh, exactly the bug this test guards against")
	}

	found := false
	for _, m := range saved.Messages {
		if m.Role == llm.RoleUser && m.Content == "hello, are you there?" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("persisted session does not contain the user's message; got messages: %+v", saved.Messages)
	}
}

// Companion test: once the turn eventually DOES complete (the agent kept
// working in the background per the detached-context fix in
// forge-ui/chat.go, even though the original HTTP client vanished), a
// second call to Execute against the SAME task ID must not need the
// original message again — it should already be recoverable from disk,
// and the session file must accumulate rather than reset.
func TestLLMExecutor_RefreshThenRetry_SessionAccumulates(t *testing.T) {
	store, err := NewMemoryStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	const taskID = "refresh-retry-task"

	// First turn: cancelled immediately (the "refresh").
	var cancel context.CancelFunc
	cancelExec := NewLLMExecutor(LLMExecutorConfig{
		Client: &mockLLMClient{
			chatFunc: func(_ context.Context, _ *llm.ChatRequest) (*llm.ChatResponse, error) {
				cancel()
				return nil, context.Canceled
			},
		},
		Tools:     &mockToolExecutor{toolDefs: []llm.ToolDefinition{}},
		Store:     store,
		ModelName: "test",
		Provider:  "test",
	})
	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	_, err = cancelExec.Execute(ctx,
		&a2a.Task{ID: taskID},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("first message")}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first Execute should be cancelled, got %v", err)
	}

	// Second turn: a fresh, un-cancelled context (simulating the agent
	// process having kept running, or the user reconnecting and the
	// session recovering from disk) completes normally.
	okExec := NewLLMExecutor(LLMExecutorConfig{
		Client: &mockLLMClient{
			chatFunc: func(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
				return &llm.ChatResponse{
					ID:      "r2",
					Message: llm.ChatMessage{Role: llm.RoleAssistant, Content: "all done"},
				}, nil
			},
		},
		Tools:     &mockToolExecutor{toolDefs: []llm.ToolDefinition{}},
		Store:     store,
		ModelName: "test",
		Provider:  "test",
	})

	_, err = okExec.Execute(context.Background(),
		&a2a.Task{ID: taskID, History: nil},
		&a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart("second message")}},
	)
	if err != nil {
		t.Fatalf("second Execute should succeed, got %v", err)
	}

	saved, loadErr := store.Load(taskID)
	if loadErr != nil {
		t.Fatalf("store.Load: %v", loadErr)
	}
	if saved == nil {
		t.Fatal("session missing after second turn")
	}

	var userMsgs []string
	for _, m := range saved.Messages {
		if m.Role == llm.RoleUser {
			userMsgs = append(userMsgs, m.Content)
		}
	}
	if len(userMsgs) != 2 {
		t.Fatalf("expected both turns' user messages preserved in one session file (accumulated, not overwritten-and-lost), got %v", userMsgs)
	}
	if userMsgs[0] != "first message" || userMsgs[1] != "second message" {
		t.Fatalf("unexpected message order/content: %v", userMsgs)
	}

	// Same task ID -> same file on disk, not a forked/orphaned session.
	ids, listErr := store.List()
	if listErr != nil {
		t.Fatalf("store.List: %v", listErr)
	}
	if len(ids) != 1 {
		t.Fatalf("expected exactly one session file on disk for this task ID, got %v", ids)
	}
}
