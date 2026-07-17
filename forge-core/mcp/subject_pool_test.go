package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/initializ/forge/forge-core/auth"
)

// fakeClient is a no-op Client for pool tests; it records Close.
type fakeClient struct{ closed atomic.Bool }

func (f *fakeClient) Initialize(context.Context, ClientInfo) (*InitializeResult, error) {
	return &InitializeResult{}, nil
}
func (f *fakeClient) Initialized(context.Context) error { return nil }
func (f *fakeClient) ListTools(context.Context) ([]MCPToolDescriptor, error) {
	return nil, nil
}
func (f *fakeClient) CallTool(context.Context, string, json.RawMessage) (*CallToolResult, error) {
	return &CallToolResult{}, nil
}
func (f *fakeClient) Close() error { f.closed.Store(true); return nil }

func ctxWithUser(email string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Email: email})
}

// TestSubjectConnPool_PerSubjectLazyEstablishAndReuse: distinct users get
// distinct connections established lazily on first use; the same user
// reuses theirs (one connect each). The connect ctx carries that user's
// identity so authFn would resolve the right token.
func TestSubjectConnPool_PerSubjectLazyEstablishAndReuse(t *testing.T) {
	var connects atomic.Int32
	seen := map[string]bool{}
	var mu sync.Mutex
	pool := newSubjectConnPool(func(ctx context.Context) (Client, error) {
		connects.Add(1)
		if id := auth.IdentityFromContext(ctx); id != nil {
			mu.Lock()
			seen[id.Email] = true
			mu.Unlock()
		}
		return &fakeClient{}, nil
	})

	if connects.Load() != 0 {
		t.Fatal("pool must not connect before first ClientFor (lazy)")
	}

	a1, err := pool.ClientFor(ctxWithUser("alice@corp.com"))
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	a2, _ := pool.ClientFor(ctxWithUser("alice@corp.com"))
	b1, _ := pool.ClientFor(ctxWithUser("bob@corp.com"))

	if a1 != a2 {
		t.Error("same user must reuse the same connection")
	}
	if a1 == b1 {
		t.Error("distinct users must get distinct connections")
	}
	if n := connects.Load(); n != 2 {
		t.Errorf("connects = %d, want 2 (one per subject; alice reused)", n)
	}
	if !seen["alice@corp.com"] || !seen["bob@corp.com"] {
		t.Errorf("connect ctx did not carry each user's identity: %v", seen)
	}
	if pool.len() != 2 {
		t.Errorf("pool has %d conns, want 2", pool.len())
	}
}

// TestSubjectConnPool_NoSubjectIsLazyErr: no authenticated user → ErrNoToken.
func TestSubjectConnPool_NoSubjectIsLazyErr(t *testing.T) {
	pool := newSubjectConnPool(func(context.Context) (Client, error) {
		t.Fatal("must not connect without a subject")
		return nil, nil
	})
	if _, err := pool.ClientFor(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no subject must yield ErrNoToken, got: %v", err)
	}
}

// TestSubjectConnPool_SingleFlight: concurrent first calls for one subject
// open exactly one connection.
func TestSubjectConnPool_SingleFlight(t *testing.T) {
	var connects atomic.Int32
	release := make(chan struct{})
	pool := newSubjectConnPool(func(context.Context) (Client, error) {
		connects.Add(1)
		<-release // hold the establish so all callers pile up behind it
		return &fakeClient{}, nil
	})

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_, _ = pool.ClientFor(ctxWithUser("carol@corp.com"))
		}()
	}
	// Give the goroutines time to queue behind the single flight, then let go.
	for pool.inFlyLen() == 0 {
	}
	close(release)
	wg.Wait()

	if c := connects.Load(); c != 1 {
		t.Errorf("connects = %d, want 1 (single-flighted per subject)", c)
	}
}

// TestSubjectConnPool_EvictReconnects: Evict drops the connection so the
// next call re-establishes.
func TestSubjectConnPool_EvictReconnects(t *testing.T) {
	var connects atomic.Int32
	pool := newSubjectConnPool(func(context.Context) (Client, error) {
		connects.Add(1)
		return &fakeClient{}, nil
	})
	ctx := ctxWithUser("dave@corp.com")
	c1, _ := pool.ClientFor(ctx)
	pool.Evict(ctx)
	if fc, ok := c1.(*fakeClient); !ok || !fc.closed.Load() {
		t.Error("Evict must close the evicted connection")
	}
	c2, _ := pool.ClientFor(ctx)
	if c1 == c2 {
		t.Error("post-evict ClientFor must re-establish a fresh connection")
	}
	if n := connects.Load(); n != 2 {
		t.Errorf("connects = %d, want 2 (establish, evict, re-establish)", n)
	}
}

// TestSubjectConnPool_CloseTearsDown: Close closes all connections and the
// pool is unusable after.
func TestSubjectConnPool_CloseTearsDown(t *testing.T) {
	pool := newSubjectConnPool(func(context.Context) (Client, error) {
		return &fakeClient{}, nil
	})
	c, _ := pool.ClientFor(ctxWithUser("erin@corp.com"))
	if err := pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fc := c.(*fakeClient); !fc.closed.Load() {
		t.Error("Close must close pooled connections")
	}
	if _, err := pool.ClientFor(ctxWithUser("erin@corp.com")); !errors.Is(err, ErrClosed) {
		t.Errorf("ClientFor after Close must be ErrClosed, got: %v", err)
	}
}
