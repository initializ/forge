package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// Client speaks the four MCP RPCs Phase 1 needs. It wraps a Transport
// with request/response demultiplexing: each call gets a fresh
// monotonic ID, sends, then waits for the matching response.
//
// Concurrency: CallTool may be invoked from multiple goroutines; the
// internal demultiplexer correctly routes responses by ID. Initialize
// must be the first call on a fresh Client — the Server lifecycle in
// server.go enforces that.
type Client interface {
	Initialize(ctx context.Context, info ClientInfo) (*InitializeResult, error)
	Initialized(ctx context.Context) error
	ListTools(ctx context.Context) ([]MCPToolDescriptor, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error)
	Close() error
}

// NewClient constructs a Client on top of the given Transport. The
// returned Client takes ownership of the Transport — Close() closes
// both. After construction, the caller MUST start the demultiplexer
// by calling Run() in its own goroutine OR by relying on the Server
// lifecycle which does this automatically.
func NewClient(t Transport) *clientImpl {
	c := &clientImpl{
		tr:      t,
		pending: make(map[string]chan *JSONRPCMessage),
		done:    make(chan struct{}),
	}
	return c
}

// clientImpl is the concrete Client. Exported only via the Client
// interface.
type clientImpl struct {
	tr Transport

	nextID atomic.Int64

	mu      sync.Mutex
	pending map[string]chan *JSONRPCMessage // id → response chan

	closed atomic.Bool
	done   chan struct{}
	once   sync.Once
}

// Run starts the response demultiplexer. Blocks until Close is called
// or the underlying transport errors. The Server lifecycle calls this
// in its own goroutine.
func (c *clientImpl) Run(ctx context.Context) {
	for {
		if c.closed.Load() {
			return
		}
		msg, err := c.tr.Recv(ctx)
		if err != nil {
			c.failAll(err)
			return
		}
		if msg.ID == nil {
			// server-initiated notification — Phase 1 ignores these
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[msg.ID.String()]
		if ok {
			delete(c.pending, msg.ID.String())
		}
		c.mu.Unlock()
		if ok {
			ch <- &msg
		}
		// Unknown id: drop. A future debug log entry could be added.
	}
}

func (c *clientImpl) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		ch <- &JSONRPCMessage{
			Jsonrpc: "2.0",
			Error: &JSONRPCError{
				Code:    -32000,
				Message: fmt.Sprintf("transport error: %v", err),
			},
		}
		close(ch)
		delete(c.pending, id)
	}
}

// roundTrip sends a request and waits for the matching response.
func (c *clientImpl) roundTrip(ctx context.Context, method string, params any) (*JSONRPCMessage, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}

	id := json.Number(fmt.Sprintf("%d", c.nextID.Add(1)))
	msg := JSONRPCMessage{Jsonrpc: "2.0", ID: &id, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("%w: marshal params: %v", ErrProtocolError, err)
		}
		msg.Params = b
	}

	ch := make(chan *JSONRPCMessage, 1)
	c.mu.Lock()
	c.pending[id.String()] = ch
	c.mu.Unlock()

	if err := c.tr.Send(ctx, msg); err != nil {
		c.mu.Lock()
		delete(c.pending, id.String())
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id.String())
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("%w: %s", ErrProtocolError, resp.Error.Message)
		}
		return resp, nil
	}
}

// notify sends a JSON-RPC notification (no response expected).
func (c *clientImpl) notify(ctx context.Context, method string, params any) error {
	if c.closed.Load() {
		return ErrClosed
	}
	msg := JSONRPCMessage{Jsonrpc: "2.0", Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%w: marshal params: %v", ErrProtocolError, err)
		}
		msg.Params = b
	}
	return c.tr.Send(ctx, msg)
}

// Initialize performs the MCP initialize handshake. Hard-fails with
// ErrVersionMismatch when the server's protocolVersion does not equal
// the pinned ProtocolVersion.
func (c *clientImpl) Initialize(ctx context.Context, info ClientInfo) (*InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ClientInfo:      info,
	}
	resp, err := c.roundTrip(ctx, MethodInitialize, params)
	if err != nil {
		return nil, err
	}
	var out InitializeResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("%w: parse InitializeResult: %v", ErrProtocolError, err)
	}
	if out.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("%w: server %s, client %s", ErrVersionMismatch, out.ProtocolVersion, ProtocolVersion)
	}
	return &out, nil
}

// Initialized emits the "initialized" notification per MCP spec.
func (c *clientImpl) Initialized(ctx context.Context) error {
	return c.notify(ctx, MethodInitialized, nil)
}

// ListTools fetches the server's tool catalog.
func (c *clientImpl) ListTools(ctx context.Context) ([]MCPToolDescriptor, error) {
	resp, err := c.roundTrip(ctx, MethodToolsList, nil)
	if err != nil {
		return nil, err
	}
	var out ListToolsResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("%w: parse ListToolsResult: %v", ErrProtocolError, err)
	}
	return out.Tools, nil
}

// CallTool invokes a single tool by name.
func (c *clientImpl) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	params := CallToolParams{Name: name, Arguments: args}
	resp, err := c.roundTrip(ctx, MethodToolsCall, params)
	if err != nil {
		return nil, err
	}
	var out CallToolResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("%w: parse CallToolResult: %v", ErrProtocolError, err)
	}
	return &out, nil
}

// Close releases the transport and unblocks any pending RPCs.
func (c *clientImpl) Close() error {
	c.once.Do(func() {
		c.closed.Store(true)
		close(c.done)
	})
	return c.tr.Close()
}
