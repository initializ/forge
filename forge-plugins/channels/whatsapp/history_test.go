package whatsapp

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func entry(id, author, text string) historyEntry {
	return historyEntry{ID: id, Author: author, Text: text, At: time.Date(2026, 9, 9, 14, 30, 0, 0, time.UTC)}
}

func TestChatHistory_RecordAndRecent(t *testing.T) {
	h := newChatHistory(5)
	h.record("chat1", entry("1", "alice", "hello"))
	h.record("chat1", entry("2", "bob", "hi"))

	got := h.recent("chat1", 5)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].Text != "hello" || got[1].Text != "hi" {
		t.Errorf("expected oldest-first order, got %v", got)
	}
}

func TestChatHistory_IsolatedPerChat(t *testing.T) {
	h := newChatHistory(5)
	h.record("chat1", entry("1", "alice", "in one"))
	h.record("chat2", entry("2", "bob", "in two"))

	if got := h.recent("chat1", 5); len(got) != 1 || got[0].Text != "in one" {
		t.Errorf("chat1 leaked: %v", got)
	}
	if got := h.recent("chat2", 5); len(got) != 1 || got[0].Text != "in two" {
		t.Errorf("chat2 leaked: %v", got)
	}
}

func TestChatHistory_TrimsToPerChatCap(t *testing.T) {
	h := newChatHistory(3)
	for i := range 10 {
		h.record("chat1", entry(strconv.Itoa(i), "alice", "msg"+strconv.Itoa(i)))
	}
	got := h.recent("chat1", 10)
	if len(got) != 3 {
		t.Fatalf("expected cap of 3, got %d", len(got))
	}
	if got[0].Text != "msg7" || got[2].Text != "msg9" {
		t.Errorf("expected the newest three, got %v", got)
	}
}

func TestChatHistory_RecentHonoursN(t *testing.T) {
	h := newChatHistory(10)
	for i := range 5 {
		h.record("chat1", entry(strconv.Itoa(i), "alice", "msg"+strconv.Itoa(i)))
	}
	if got := h.recent("chat1", 2); len(got) != 2 || got[1].Text != "msg4" {
		t.Errorf("expected the newest two, got %v", got)
	}
	if got := h.recent("chat1", 0); got != nil {
		t.Errorf("expected nil for n=0, got %v", got)
	}
}

func TestChatHistory_SkipsEmptyText(t *testing.T) {
	h := newChatHistory(5)
	h.record("chat1", entry("1", "alice", "   "))
	h.record("", entry("2", "alice", "no chat"))
	if got := h.recent("chat1", 5); len(got) != 0 {
		t.Errorf("expected empty text skipped, got %v", got)
	}
}

// An account in many groups must not grow the ring without bound.
func TestChatHistory_EvictsLeastRecentChat(t *testing.T) {
	h := newChatHistory(2)
	for i := range maxTrackedChats + 10 {
		h.record("chat"+strconv.Itoa(i), entry("m", "alice", "text"))
		// Distinct touch times so eviction order is deterministic.
		h.mu.Lock()
		h.touched["chat"+strconv.Itoa(i)] = time.Now().Add(time.Duration(i) * time.Millisecond)
		h.mu.Unlock()
	}
	h.mu.Lock()
	tracked := len(h.perChat)
	h.mu.Unlock()

	if tracked > maxTrackedChats {
		t.Errorf("expected at most %d chats tracked, got %d", maxTrackedChats, tracked)
	}
	if got := h.recent("chat0", 5); len(got) != 0 {
		t.Errorf("expected the oldest chat evicted, got %v", got)
	}
}

// recent returns a copy; a caller mutating it must not corrupt the ring.
func TestChatHistory_RecentReturnsCopy(t *testing.T) {
	h := newChatHistory(5)
	h.record("chat1", entry("1", "alice", "original"))

	got := h.recent("chat1", 5)
	got[0].Text = "mutated"

	if again := h.recent("chat1", 5); again[0].Text != "original" {
		t.Errorf("caller mutation leaked into the ring: %q", again[0].Text)
	}
}

func TestChatHistory_ConcurrentRecord(t *testing.T) {
	h := newChatHistory(50)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.record("chat1", entry(strconv.Itoa(i), "alice", "msg"))
		}()
	}
	wg.Wait()
	if got := h.recent("chat1", 100); len(got) != 50 {
		t.Errorf("expected 50 entries after cap, got %d", len(got))
	}
}

func TestPrependHistory_BuildsContextBlock(t *testing.T) {
	entries := []historyEntry{
		entry("1", "alice", "deploy failed"),
		entry("2", "bob", "looking now"),
		entry("3", "alice", "@agent summarise"),
	}
	got := prependHistory(entries, "3", "summarise")

	if !strings.Contains(got, "deploy failed") || !strings.Contains(got, "looking now") {
		t.Errorf("expected prior messages in block, got %q", got)
	}
	if strings.Contains(got, "@agent summarise") {
		t.Errorf("current message must be skipped inside its own context, got %q", got)
	}
	if !strings.HasSuffix(got, "summarise") {
		t.Errorf("prompt must come last, got %q", got)
	}
	if strings.Index(got, "deploy failed") > strings.Index(got, "looking now") {
		t.Errorf("expected oldest-first ordering, got %q", got)
	}
}

func TestPrependHistory_EmptyReturnsPromptUnchanged(t *testing.T) {
	if got := prependHistory(nil, "1", "hello"); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// Skipping the only entry leaves nothing to prepend.
func TestPrependHistory_OnlyCurrentMessage(t *testing.T) {
	entries := []historyEntry{entry("1", "alice", "hello")}
	if got := prependHistory(entries, "1", "hello"); got != "hello" {
		t.Errorf("got %q, want the bare prompt", got)
	}
}

// A chatty group must not crowd the prompt out of the context budget.
func TestPrependHistory_SoftCap(t *testing.T) {
	var entries []historyEntry
	for i := range 500 {
		entries = append(entries, entry(strconv.Itoa(i), "alice", strings.Repeat("x", 100)))
	}
	got := prependHistory(entries, "none", "the prompt")

	if len(got) > historySoftCap+500 {
		t.Errorf("block exceeded soft cap: %d chars", len(got))
	}
	if !strings.HasSuffix(got, "the prompt") {
		t.Error("prompt must survive the cap")
	}
}

// The cap counts backwards from the newest, so the most recent context is the
// part that survives.
func TestPrependHistory_CapKeepsNewest(t *testing.T) {
	var entries []historyEntry
	for i := range 500 {
		entries = append(entries, entry(strconv.Itoa(i), "alice", strings.Repeat("x", 100)+strconv.Itoa(i)))
	}
	got := prependHistory(entries, "none", "prompt")

	if !strings.Contains(got, "x499") {
		t.Error("expected the newest entry retained under the cap")
	}
	if strings.Contains(got, "x0\n") {
		t.Error("expected the oldest entry dropped under the cap")
	}
}
