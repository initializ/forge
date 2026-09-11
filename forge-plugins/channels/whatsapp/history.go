package whatsapp

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// maxTrackedChats bounds how many conversations the history ring holds before
// evicting the least recently active. Without it, an account in many groups
// grows the ring without limit.
const maxTrackedChats = 200

// historyEntry is one observed message, kept for conversational context.
type historyEntry struct {
	ID     string
	Author string
	Text   string
	At     time.Time
}

// chatHistory is a bounded, in-memory record of recently observed messages,
// keyed by chat JID.
//
// This exists because WhatsApp has no server-side "fetch recent messages" call
// the way Microsoft Graph does (/chats/{id}/messages). Prior history reaches a
// linked device only through a history-sync push at pairing time, and
// whatsmeow does not retain it. So the adapter accumulates its own window from
// the traffic it sees.
//
// The consequence is worth stating plainly: context covers messages observed
// SINCE THE ADAPTER STARTED, and is lost on restart. An agent restarted
// mid-conversation will not see what came before. That is a real limitation of
// the protocol, not a shortcut — persisting it would mean writing a message
// store, which is out of scope for the channel adapter.
type chatHistory struct {
	mu      sync.Mutex
	perChat map[string][]historyEntry
	touched map[string]time.Time
	max     int
}

func newChatHistory(maxPerChat int) *chatHistory {
	if maxPerChat <= 0 {
		maxPerChat = 20
	}
	return &chatHistory{
		perChat: make(map[string][]historyEntry),
		touched: make(map[string]time.Time),
		max:     maxPerChat,
	}
}

// record appends an entry to a chat's window, trimming the oldest beyond the
// per-chat cap and evicting the least recently active chat when tracking too
// many.
func (h *chatHistory) record(chat string, e historyEntry) {
	if chat == "" || strings.TrimSpace(e.Text) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	entries := append(h.perChat[chat], e)
	if len(entries) > h.max {
		entries = entries[len(entries)-h.max:]
	}
	h.perChat[chat] = entries
	h.touched[chat] = time.Now()

	if len(h.perChat) > maxTrackedChats {
		h.evictOldestLocked()
	}
}

// evictOldestLocked drops the least recently active chat. Caller holds mu.
func (h *chatHistory) evictOldestLocked() {
	var oldest string
	var oldestAt time.Time
	for chat, at := range h.touched {
		if oldest == "" || at.Before(oldestAt) {
			oldest, oldestAt = chat, at
		}
	}
	if oldest != "" {
		delete(h.perChat, oldest)
		delete(h.touched, oldest)
	}
}

// recent returns up to n of a chat's most recent entries, oldest first.
func (h *chatHistory) recent(chat string, n int) []historyEntry {
	h.mu.Lock()
	defer h.mu.Unlock()

	entries := h.perChat[chat]
	if n <= 0 || len(entries) == 0 {
		return nil
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	out := make([]historyEntry, len(entries))
	copy(out, entries)
	return out
}

// prependHistory formats a chat's recent messages chronologically and prepends
// them as a context block before the user's current prompt. skipID drops the
// current message so it isn't duplicated inside its own context.
//
// The block is soft-capped at historySoftCap characters, counted from the most
// recent message backwards, so a chatty group can't crowd out the prompt.
func prependHistory(entries []historyEntry, skipID, prompt string) string {
	if len(entries) == 0 {
		return prompt
	}

	// Walk newest→oldest accumulating under the cap, then emit oldest→newest.
	var kept []string
	total := 0
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.ID == skipID {
			continue
		}
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		line := fmt.Sprintf("%s  %s: %s\n", e.At.Format("01-02 15:04"), e.Author, text)
		if total+len(line) > historySoftCap {
			break
		}
		total += len(line)
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return prompt
	}

	var b strings.Builder
	b.WriteString("[Recent chat history for context — most recent message at the bottom:]\n")
	for i := len(kept) - 1; i >= 0; i-- {
		b.WriteString(kept[i])
	}
	b.WriteString("[End of history. The user's current message follows:]\n\n")
	b.WriteString(prompt)
	return b.String()
}
