package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// TestAuthAudit_SeqStampedWhenCounterInstalled is the #174 regression
// pin: when the request's ctx carries a SequenceCounter (as it does
// after installSequenceCounterMiddleware wraps the auth chain),
// makeAuthAuditCallback's emit picks the counter up via
// EmitFromContext and stamps seq=1 on auth_verify.
//
// Pre-fix the callback used plain Emit and lost seq entirely.
func TestAuthAudit_SeqStampedWhenCounterInstalled(t *testing.T) {
	var buf bytes.Buffer
	cb := makeAuthAuditCallback(coreruntime.NewAuditLogger(&buf))

	req := httptest.NewRequest(http.MethodPost, "/tasks", nil)
	// Simulate the wrapper: install a fresh counter on req.Context().
	ctx := coreruntime.WithSequenceCounter(req.Context(), new(coreruntime.SequenceCounter))
	req = req.WithContext(ctx)

	id := &auth.Identity{UserID: "alice", Source: "oidc"}
	cb(req, id, nil, "jwt")

	var ev coreruntime.AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Event != coreruntime.EventAuthVerify {
		t.Fatalf("Event = %q, want auth_verify", ev.Event)
	}
	if ev.Sequence != 1 {
		t.Errorf("auth_verify seq = %d, want 1 (counter installed pre-auth)", ev.Sequence)
	}
}

// TestAuthAudit_NoSeqWhenCounterAbsent confirms the no-counter path
// stays valid: when nothing installed a counter on ctx, the emit
// produces an event with seq=0 (and the omitempty JSON tag drops the
// field). This pins backward-compat for legacy embedders that wire
// their own server.Server without the wrapper.
func TestAuthAudit_NoSeqWhenCounterAbsent(t *testing.T) {
	var buf bytes.Buffer
	cb := makeAuthAuditCallback(coreruntime.NewAuditLogger(&buf))

	req := httptest.NewRequest(http.MethodPost, "/tasks", nil) // no counter on ctx
	cb(req, &auth.Identity{UserID: "alice", Source: "oidc"}, nil, "jwt")

	body := strings.TrimSpace(buf.String())
	if strings.Contains(body, `"seq"`) {
		t.Errorf("seq field must be omitted when no counter is on ctx; got: %s", body)
	}
}

// TestSequenceCounterMiddleware_InstallsCounterBeforeNext verifies the
// wrapper installs the counter on r.Context() before delegating to
// the wrapped middleware (and through to the next handler). The next
// handler reads the counter off the context to confirm.
func TestSequenceCounterMiddleware_InstallsCounterBeforeNext(t *testing.T) {
	// A passthrough auth middleware — just calls next.
	passthroughAuth := func(next http.Handler) http.Handler { return next }

	var observed *coreruntime.SequenceCounter
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = coreruntime.SequenceCounterFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrapped := installSequenceCounterMiddleware(passthroughAuth)(terminal)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if observed == nil {
		t.Fatal("terminal handler saw no SequenceCounter on ctx")
	}
	// Counter starts at 0 and increments to 1 on first NextSequence call.
	if got := coreruntime.NextSequence(coreruntime.WithSequenceCounter(context.Background(), observed)); got != 1 {
		t.Errorf("first NextSequence on wrapper-installed counter = %d, want 1", got)
	}
}

// TestEnsureSequenceCounter_ReusesExisting pins the runner-side
// invariant: the per-A2A-request setup must NOT clobber a counter
// already installed by the auth wrapper. EnsureSequenceCounter
// returns ctx unchanged when the counter is already present.
func TestEnsureSequenceCounter_ReusesExisting(t *testing.T) {
	original := new(coreruntime.SequenceCounter)
	ctx := coreruntime.WithSequenceCounter(context.Background(), original)
	// Advance the counter once so we can detect a reset.
	_ = coreruntime.NextSequence(ctx)

	ctx2 := coreruntime.EnsureSequenceCounter(ctx)

	got := coreruntime.SequenceCounterFromContext(ctx2)
	if got != original {
		t.Errorf("EnsureSequenceCounter replaced the existing counter; want pointer-equality")
	}
	// The counter must continue from where it left off (seq=2 next).
	if next := coreruntime.NextSequence(ctx2); next != 2 {
		t.Errorf("counter reset by EnsureSequenceCounter; got next=%d, want 2", next)
	}
}

// TestEnsureSequenceCounter_InstallsFresh covers the --no-auth path
// where the wrapper never ran: EnsureSequenceCounter installs a
// fresh counter so per-A2A-request audit emit still gets seq stamped.
func TestEnsureSequenceCounter_InstallsFresh(t *testing.T) {
	ctx := coreruntime.EnsureSequenceCounter(context.Background())
	if coreruntime.SequenceCounterFromContext(ctx) == nil {
		t.Fatal("EnsureSequenceCounter on empty ctx should install a counter")
	}
	if next := coreruntime.NextSequence(ctx); next != 1 {
		t.Errorf("fresh counter's first NextSequence = %d, want 1", next)
	}
}
