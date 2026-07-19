package mcp

import (
	"sync"
	"time"
)

// SubjectTokenStore caches delegated access tokens per requesting user
// (#317/#330). It is the seam §18.8 #2 calls out: the default is
// process-memory (memSubjectTokenStore), but a managed broker can substitute
// a shared/durable implementation so per-user grants survive restarts and
// are shared across replicas, without the resolver or the agent changing.
//
// Contract:
//   - Get must NOT return an expired token — freshness (including any early-
//     refresh skew) is the store's policy, so the resolver just asks and
//     trusts the answer.
//   - The store holds only ACCESS tokens (short-lived). Refresh tokens never
//     reach it — they never leave the broker (invariant 8).
//   - Implementations must be safe for concurrent use by many goroutines
//     (distinct users resolve in parallel — the whole point of #317).
type SubjectTokenStore interface {
	// Get returns a cached, unexpired access token for subject, or ok=false
	// when there is none (or it is too close to expiry to reuse).
	Get(subject string) (token string, ok bool)
	// Put stores token for subject, valid for ttl from now.
	Put(subject, token string, ttl time.Duration)
	// Evict drops any cached token for subject (e.g. on a 401 from the
	// downstream server — a stale token must not linger).
	Evict(subject string)
}

// NewInMemorySubjectTokenStore returns the default in-process
// SubjectTokenStore. Exported for the standalone consent wiring (#332) to
// create the shared store its resolver reads and its callback writes.
func NewInMemorySubjectTokenStore() SubjectTokenStore {
	return newMemSubjectTokenStore(0)
}

// memSubjectTokenStore is the default in-process SubjectTokenStore: a
// per-subject map with early-refresh skew and an opportunistic sweep so it
// can't grow unbounded with one-shot users, without a background goroutine.
type memSubjectTokenStore struct {
	mu   sync.Mutex
	m    map[string]cachedToken
	skew time.Duration
	now  func() time.Time
}

// newMemSubjectTokenStore builds the default store. skew<=0 uses
// platformTokenSkew (re-fetch slightly before nominal expiry so a token
// never expires mid-flight downstream).
func newMemSubjectTokenStore(skew time.Duration) *memSubjectTokenStore {
	if skew <= 0 {
		skew = platformTokenSkew
	}
	return &memSubjectTokenStore{m: make(map[string]cachedToken), skew: skew, now: time.Now}
}

func (s *memSubjectTokenStore) Get(subject string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[subject]
	if !ok {
		return "", false
	}
	if c.token != "" && s.now().Before(c.expiresAt.Add(-s.skew)) {
		return c.token, true
	}
	// Stale — evict eagerly so a sensitive token isn't held past use
	// (#327 review finding 2).
	delete(s.m, subject)
	return "", false
}

func (s *memSubjectTokenStore) Put(subject, token string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// Opportunistic sweep so the map can't grow unbounded with one-shot
	// users' stale tokens — a background-free TTL bound (#327 review).
	for sub, c := range s.m {
		if !now.Before(c.expiresAt) {
			delete(s.m, sub)
		}
	}
	s.m[subject] = cachedToken{token: token, expiresAt: now.Add(ttl)}
}

func (s *memSubjectTokenStore) Evict(subject string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, subject)
}
