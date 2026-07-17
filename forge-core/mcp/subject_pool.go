package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// ClientResolver resolves which MCP Client a tool call should use. The
// per-call ctx carries the requesting user's identity, so a per-subject
// pool can route to that user's own connection (#317 connection
// lifecycle). Servers that don't need per-user connections use a
// StaticResolver returning the one shared Client.
type ClientResolver interface {
	ClientFor(ctx context.Context) (Client, error)
}

var (
	_ ClientResolver = (*subjectConnPool)(nil)
	_ ClientResolver = StaticResolver{}
)

// StaticResolver returns one fixed Client for every call — the shared
// single-connection behavior for bearer/static/agent-principal/discovered
// -oauth servers, where one identity serves all requests. It is the
// default the tool-routing seam uses so non-per-user servers behave
// exactly as before.
type StaticResolver struct{ Client Client }

func (s StaticResolver) ClientFor(context.Context) (Client, error) {
	if s.Client == nil {
		return nil, fmt.Errorf("%w: no MCP client available", ErrTransportUnavailable)
	}
	return s.Client, nil
}

// poolEstablishTimeout bounds a single per-subject connect+initialize.
const poolEstablishTimeout = 30 * time.Second

// subjectConnPool maintains one lazily-established MCP connection per
// requesting-user subject (#317). A type=user server binds identity at
// initialize, so each user needs their own connection: the pool
// establishes it on first use — running connect (factory + Initialize)
// under that user's identity so authFn resolves THEIR token at
// initialize — and reuses it thereafter.
//
// Establishment is single-flighted per subject (a burst of a user's first
// calls opens exactly one connection); distinct subjects never block each
// other. Concurrency-safe; Close tears down every connection.
type subjectConnPool struct {
	// connect establishes a fresh connection. Its ctx carries the
	// subject's identity but is decoupled from any caller's cancellation
	// (background-derived + a hard timeout) so one caller's cancel can't
	// tear down the shared connection mid-establish (the B2 lesson).
	connect func(ctx context.Context) (Client, error)

	mu     sync.Mutex
	conns  map[string]Client
	inFly  map[string]*connFlight
	closed bool
	wg     sync.WaitGroup // tracks in-flight establishes for a synchronous Close
}

type connFlight struct {
	done   chan struct{}
	client Client
	err    error
}

func newSubjectConnPool(connect func(ctx context.Context) (Client, error)) *subjectConnPool {
	return &subjectConnPool{
		connect: connect,
		conns:   map[string]Client{},
		inFly:   map[string]*connFlight{},
	}
}

// ClientFor returns the connection for the requesting user in ctx,
// establishing it on first use. No authenticated user → ErrNoToken
// (lazy; a delegated connection is never established at startup).
func (p *subjectConnPool) ClientFor(ctx context.Context) (Client, error) {
	subject := delegatedSubject(ctx)
	if subject == "" {
		return nil, fmt.Errorf("%w: no requesting user in context — a delegated MCP connection is established lazily under a user's session", ErrNoToken)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if c, ok := p.conns[subject]; ok {
		p.mu.Unlock()
		return c, nil
	}
	fl, exists := p.inFly[subject]
	if !exists {
		fl = &connFlight{done: make(chan struct{})}
		p.inFly[subject] = fl
		p.mu.Unlock()

		// Preserve the subject's identity on a background-derived,
		// bounded ctx so authFn resolves the right user's token but a
		// caller's cancel can't abort the shared establish.
		connCtx, cancel := context.WithTimeout(context.Background(), poolEstablishTimeout)
		if id := auth.IdentityFromContext(ctx); id != nil {
			connCtx = auth.WithIdentity(connCtx, id)
		}
		p.wg.Add(1)
		go func() {
			var (
				c   Client
				err error
			)
			// Unconditionally clear the flight, cache-or-close the result,
			// and unblock waiters — even if p.connect panics. Without this,
			// a panicking factory/Initialize would leave inFly[subject]
			// populated and fl.done open, wedging EVERY future call for
			// that subject on a dead flight (#329 review finding 1).
			defer func() {
				if r := recover(); r != nil {
					c, err = nil, fmt.Errorf("%w: connect panicked: %v", ErrProtocolError, r)
				}
				p.mu.Lock()
				delete(p.inFly, subject)
				switch {
				case err != nil:
					// leave nothing cached
				case p.closed:
					// pool closed mid-establish — drop it
					if c != nil {
						_ = c.Close()
					}
					c, err = nil, ErrClosed
				default:
					p.conns[subject] = c
				}
				p.mu.Unlock()
				fl.client, fl.err = c, err
				close(fl.done)
				cancel()
				p.wg.Done()
			}()
			c, err = p.connect(connCtx)
		}()
	} else {
		p.mu.Unlock()
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-fl.done:
	}
	return fl.client, fl.err
}

// Evict drops (and closes) the connection for a subject — call when a
// request on it fails with a connection error so the next call
// re-establishes. Idempotent.
func (p *subjectConnPool) Evict(ctx context.Context) {
	subject := delegatedSubject(ctx)
	if subject == "" {
		return
	}
	p.mu.Lock()
	c, ok := p.conns[subject]
	delete(p.conns, subject)
	p.mu.Unlock()
	if ok && c != nil {
		_ = c.Close()
	}
}

// Close tears down every pooled connection synchronously. It first marks
// the pool closed (so any in-flight establish self-closes on completion),
// waits for those establishes to finish, then closes the cached
// connections — so when Close returns, no connection remains open and no
// establish is still running (#329 review finding 2). The pool is
// unusable after.
func (p *subjectConnPool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	// Join in-flight establishes: each sees p.closed and drops its own
	// connection, so after Wait nothing new lands in p.conns.
	p.wg.Wait()

	p.mu.Lock()
	conns := p.conns
	p.conns = map[string]Client{}
	p.mu.Unlock()
	for _, c := range conns {
		if c != nil {
			_ = c.Close()
		}
	}
	return nil
}

// len reports the number of live connections (for tests / metrics).
func (p *subjectConnPool) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// inFlyLen reports in-flight establishments (test-only).
func (p *subjectConnPool) inFlyLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.inFly)
}
