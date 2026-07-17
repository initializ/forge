package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// Manager owns the set of Server instances declared in a forge.yaml
// mcp.servers[] block. Manager.Start spawns one goroutine per server
// (parallel startup); Start returns only after every server has
// reached Ready or terminally Failed.
//
// If any Required=true server fails, Start cancels the shared child
// context — all other servers tear down — and returns a non-nil
// error. The caller (runner.go) propagates this upward, exiting the
// agent with non-zero status (K8s sees CrashLoopBackOff).
//
// Tools() aggregates discovered tools across Ready servers into a
// flat list suitable for registry registration. The caller is
// responsible for wrapping each descriptor in an adapter.MCPTool —
// done in runner.go to avoid a circular dependency between
// forge-core/mcp and forge-core/tools/adapters.
type Manager struct {
	cfg     types.MCPConfig
	deps    ManagerDeps
	servers map[string]*Server

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// ManagerDeps groups the dependencies Manager hands to each Server.
type ManagerDeps struct {
	// HTTPClient is the egress-controlled client used for all MCP
	// HTTP traffic. Required — runner.go passes
	// security.EgressClientFromContext(ctx) here.
	HTTPClient *http.Client

	// Logger is used for non-audit warnings. nil → no-op.
	Logger ServerLogger

	// Audit emits mcp_server_* / mcp_tool_* events. Required for
	// production wiring; tests may pass nil.
	Audit *runtime.AuditLogger

	// OAuth is the shared OAuthFlow used by any server with
	// auth.type=oauth. Required when at least one such server exists
	// in cfg; otherwise may be nil.
	OAuth *OAuthFlow

	// Platform is the managed token-resolver wiring (ForgeConfig.Platform),
	// required by servers with auth.type=platform; otherwise may be nil.
	// MUST stay field-identical with ServerDeps (type conversion below).
	Platform *types.PlatformConfig
}

// NewManager constructs a Manager. Fails fast if config validation
// would not catch a misconfiguration here.
func NewManager(cfg types.MCPConfig, deps ManagerDeps) (*Manager, error) {
	if deps.HTTPClient == nil {
		return nil, fmt.Errorf("%w: ManagerDeps.HTTPClient is required", ErrProtocolError)
	}
	if deps.Logger == nil {
		deps.Logger = nopLogger{}
	}
	servers := make(map[string]*Server, len(cfg.Servers))
	// ManagerDeps and ServerDeps have identical fields by design —
	// they're separate types so future ServerDeps additions don't
	// force ManagerDeps callers to learn new knobs.
	srvDeps := ServerDeps(deps)
	for _, spec := range cfg.Servers {
		srv, err := NewServer(spec, srvDeps)
		if err != nil {
			return nil, fmt.Errorf("mcp manager: server %q: %w", spec.Name, err)
		}
		servers[spec.Name] = srv
	}
	return &Manager{cfg: cfg, deps: deps, servers: servers}, nil
}

// Start launches every server in parallel and blocks until each has
// reached Ready OR Failed. Returns nil when every Required server
// reached Ready; non-nil error when any Required server reached
// Failed (with the underlying cause wrapped for diagnosis).
//
// After Start returns nil, the per-server goroutines remain alive
// to handle reconnects until Stop is called.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("mcp manager: already started")
	}
	m.started = true
	m.mu.Unlock()

	if len(m.servers) == 0 {
		return nil
	}

	childCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	type result struct {
		name string
		err  error
	}

	// Spawn one Run goroutine per server. Its return value isn't
	// consumed by anyone — Required-server failures are detected via
	// the readiness channel below (which observes s.Failed()) and
	// terminal Run errors after Start has returned are immaterial
	// (the next caller of Manager.Stop would tear everything down).
	// The previous code wrote results into a buffered `errs` channel
	// that nothing ever read, with a dead-drain goroutine after
	// wg.Wait — both removed (review B10).
	for _, srv := range m.servers {
		s := srv
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			_ = s.Run(childCtx)
		}()
	}

	// Wait for each server to reach Ready or Failed, in parallel.
	readiness := make(chan result, len(m.servers))
	for _, srv := range m.servers {
		s := srv
		go func() {
			select {
			case <-s.Ready():
				readiness <- result{name: s.Name, err: nil}
			case <-s.Failed():
				readiness <- result{name: s.Name, err: fmt.Errorf("server %q failed before Ready", s.Name)}
			case <-childCtx.Done():
				readiness <- result{name: s.Name, err: childCtx.Err()}
			}
		}()
	}

	var firstRequiredErr error
	for range len(m.servers) {
		r := <-readiness
		if r.err != nil {
			srv := m.servers[r.name]
			if srv.Spec.Required {
				if firstRequiredErr == nil {
					firstRequiredErr = fmt.Errorf("mcp server %q (required): %w", r.name, r.err)
				}
				m.deps.Logger.Error("required mcp server failed during startup", map[string]any{
					"server": r.name, "error": r.err.Error(),
				})
			} else {
				m.deps.Logger.Warn("non-required mcp server failed during startup", map[string]any{
					"server": r.name, "error": r.err.Error(),
				})
			}
		}
	}

	// If a Required server failed, tear everything down before returning.
	// m.wg.Wait() blocks until every Run() goroutine has finished
	// writing to errs; the buffered channel is then unreferenced once
	// Start returns and gets collected normally. No explicit drain
	// needed (review B10 — removed dead drain goroutine that ran
	// AFTER Wait already proved all writers were done).
	if firstRequiredErr != nil {
		cancel()
		m.wg.Wait()
		return firstRequiredErr
	}

	// All Required servers are Ready. Non-required servers either
	// reached Ready or failed silently — that's fine. Return.
	return nil
}

// Stop cancels the shared context and waits for all server
// goroutines to exit. Idempotent — calling Stop twice is a no-op.
func (m *Manager) Stop() error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	return nil
}

// Tools returns a flat list of (server, descriptor, client) tuples
// for every Ready server. Callers wrap each tuple in an
// adapters.MCPTool. Order across servers is deterministic by server
// name; order within a server is the discovery order.
type ToolHandle struct {
	Server     string
	Descriptor MCPToolDescriptor
	Client     Client
}

func (m *Manager) Tools() []ToolHandle {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return nil
	}
	// Deterministic order — important for stable audit/log output.
	names := make([]string, 0, len(m.servers))
	for n := range m.servers {
		names = append(names, n)
	}
	// Manual sort to avoid importing "sort" for one call; small N.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}

	var out []ToolHandle
	for _, n := range names {
		srv := m.servers[n]
		if srv.State() != StateReady && srv.State() != StateCalling && srv.State() != StateDegraded && srv.State() != StateReconnecting {
			// Server isn't Ready (e.g. non-required failure); skip.
			continue
		}
		client := srv.Client()
		if client == nil {
			continue
		}
		for _, d := range srv.Tools() {
			out = append(out, ToolHandle{Server: n, Descriptor: d, Client: client})
		}
	}
	return out
}

// Servers returns the underlying Server objects keyed by name.
// Exposed for `forge mcp list` and tests.
func (m *Manager) Servers() map[string]*Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*Server, len(m.servers))
	for n, s := range m.servers {
		out[n] = s
	}
	return out
}
