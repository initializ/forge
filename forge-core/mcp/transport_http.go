package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// AuthTokenFunc returns a Bearer token to attach to outbound MCP
// requests. Returning ("", nil) means "no auth header" — typical for
// in-cluster trust networks. The function is invoked PER REQUEST so
// the OAuthFlow can transparently refresh expired tokens.
type AuthTokenFunc func(ctx context.Context) (string, error)

// HTTPTransport speaks the MCP Streamable HTTP transport: every
// frame is a JSON-RPC 2.0 POST to a single endpoint. The server
// MAY upgrade the response to text/event-stream when it wants to
// emit multiple frames (e.g., progress events from a long tool call).
//
// Caller is responsible for providing the *http.Client — typically
// security.EgressClientFromContext(ctx) so requests ride the Forge
// egress allowlist. HTTPTransport never constructs its own client.
//
// Concurrency: Send is safe to call from multiple goroutines. Recv
// is intended to be called from a single demultiplexer goroutine in
// the Client (a future refactor could lift this restriction if
// server-initiated frames become common).
//
// Backpressure (review B3): the response queue is bounded. When it
// fills, push() blocks (it does NOT silently drop the oldest frame
// — the previous behavior, which orphaned the in-flight CallTool
// whose response was discarded). If the queue stays full longer
// than overflowTimeout (default 30s) the transport is closed with
// ErrTransportUnavailable so callers fail loudly instead of hanging
// on their per-ID response channels.
type HTTPTransport struct {
	client     *http.Client
	url        string
	authFn     AuthTokenFunc
	clientInfo string // "<name>/<version>" for User-Agent (best-effort)

	mu        sync.Mutex
	sessionID string

	queue           chan JSONRPCMessage
	done            chan struct{}
	closeOnce       sync.Once
	overflowTimeout time.Duration // default 30s; tests may shorten
}

// NewHTTPTransport constructs an HTTPTransport. authFn may be nil for
// unauthenticated servers.
//
// The httpClient argument MUST come from the caller (e.g., Manager
// passes in security.EgressClientFromContext). We do not default to
// http.DefaultClient — that would silently bypass the egress
// enforcer.
func NewHTTPTransport(url string, httpClient *http.Client, authFn AuthTokenFunc) (*HTTPTransport, error) {
	if url == "" {
		return nil, fmt.Errorf("%w: url is empty", ErrProtocolError)
	}
	if httpClient == nil {
		return nil, fmt.Errorf("%w: httpClient is nil — caller must inject one (typically security.EgressClient)", ErrProtocolError)
	}
	return &HTTPTransport{
		client:          httpClient,
		url:             url,
		authFn:          authFn,
		clientInfo:      "forge-mcp/0.12.0",
		queue:           make(chan JSONRPCMessage, 16),
		done:            make(chan struct{}),
		overflowTimeout: 30 * time.Second,
	}, nil
}

// SessionID returns the MCP session ID assigned by the server on
// initialize. Empty until the first response carries an
// Mcp-Session-Id header. Exposed for diagnostics; the transport
// handles round-tripping internally.
func (h *HTTPTransport) SessionID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionID
}

// Send posts a JSON-RPC frame and consumes the HTTP response,
// pushing any returned frames (zero, one, or many for SSE upgrade)
// onto the internal queue for Recv.
//
// 202 Accepted with empty body is treated as a successful
// notification ack — no frames are queued.
func (h *HTTPTransport) Send(ctx context.Context, msg JSONRPCMessage) error {
	select {
	case <-h.done:
		return ErrClosed
	default:
	}
	if msg.Jsonrpc == "" {
		msg.Jsonrpc = "2.0"
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: marshaling frame: %v", ErrProtocolError, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: building request: %v", ErrTransportUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", h.clientInfo)

	h.mu.Lock()
	if h.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", h.sessionID)
	}
	h.mu.Unlock()

	if h.authFn != nil {
		tok, err := h.authFn(ctx)
		if err != nil {
			return err // already wrapped with sentinel (typically ErrTokenRevoked)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	resp, err := h.client.Do(req)
	if err != nil {
		// Distinguish ctx cancel vs. network error so caller can
		// classify reason codes correctly.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrTransportUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Capture session id if present. MCP spec: the server assigns a
	// session ID on initialize and the client echoes it on every
	// subsequent request — it is stable for the lifetime of the
	// connection. A server that rotates the value mid-stream is
	// either buggy or hostile (e.g. trying to splice Forge onto a
	// different session). Capture only on first sight; reject any
	// inconsistent change (review B7).
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.mu.Lock()
		switch h.sessionID {
		case "":
			h.sessionID = sid
		case sid:
			// unchanged — common case, accept silently.
		default:
			existing := h.sessionID
			h.mu.Unlock()
			return fmt.Errorf("%w: server rotated Mcp-Session-Id mid-stream (had %q, got %q) — possible session hijack",
				ErrProtocolError, existing, sid)
		}
		h.mu.Unlock()
	}

	switch {
	case resp.StatusCode == http.StatusAccepted:
		// 202 — notification acked, no frames expected.
		return nil

	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return h.consumeResponse(ctx, resp)

	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// Auth-time rejection. Pull the body for a hint but don't log it.
		summary := readSummary(resp.Body)
		return fmt.Errorf("%w: HTTP %d — %s", ErrProtocolError, resp.StatusCode, summary)

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		summary := readSummary(resp.Body)
		return fmt.Errorf("%w: HTTP %d — %s", ErrProtocolError, resp.StatusCode, summary)

	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: HTTP %d", ErrTransportUnavailable, resp.StatusCode)

	default:
		return fmt.Errorf("%w: HTTP %d (unexpected)", ErrTransportUnavailable, resp.StatusCode)
	}
}

// consumeResponse drains the response body into the queue. Branch on
// content-type to decide single-frame JSON vs. SSE event stream.
func (h *HTTPTransport) consumeResponse(ctx context.Context, resp *http.Response) error {
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "text/event-stream"):
		return h.consumeSSE(ctx, resp.Body)
	case strings.Contains(ct, "application/json"), ct == "":
		return h.consumeJSON(resp.Body)
	default:
		return fmt.Errorf("%w: unexpected Content-Type %q", ErrProtocolError, ct)
	}
}

// consumeJSON reads a single JSON-RPC frame and queues it.
func (h *HTTPTransport) consumeJSON(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, 16<<20)) // 16 MiB cap
	if err != nil {
		return fmt.Errorf("%w: reading body: %v", ErrTransportUnavailable, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil // empty 200 — treat as ack
	}
	var msg JSONRPCMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("%w: parsing JSON-RPC frame: %v", ErrProtocolError, err)
	}
	if err := msg.Validate(); err != nil {
		return err
	}
	// Propagate push errors (queue overflow → transport closed). Without
	// this, an overflow would have been silently swallowed by the
	// previous void-returning push (review B3).
	return h.push(msg)
}

// consumeSSE parses Server-Sent Events frames per the MCP spec —
// each `data:` line is a complete JSON-RPC frame; consecutive `data:`
// lines on a single event are joined with newline.
func (h *HTTPTransport) consumeSSE(ctx context.Context, body io.Reader) error {
	r := bufio.NewReader(body)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		var msg JSONRPCMessage
		if err := json.Unmarshal([]byte(data.String()), &msg); err != nil {
			return fmt.Errorf("%w: parsing SSE frame: %v", ErrProtocolError, err)
		}
		if err := msg.Validate(); err != nil {
			return err
		}
		if err := h.push(msg); err != nil {
			return err // overflow → transport closed (review B3)
		}
		data.Reset()
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := r.ReadString('\n')
		if err == io.EOF {
			// Final event with no terminating blank line.
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: reading SSE: %v", ErrTransportUnavailable, err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// Comment — ignore (heartbeats).
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		default:
			// Other SSE fields (event:, id:, retry:) — we ignore in MCP.
		}
	}
}

// push enqueues a frame. Blocks if the queue is full, applying
// backpressure to the SSE parser / HTTP body reader. If the queue
// stays full longer than overflowTimeout the transport is closed
// with ErrTransportUnavailable — the previous behavior (silently
// dropping the OLDEST frame, review B3) orphaned the in-flight
// CallTool whose response was discarded; this version surfaces
// the problem loudly so callers see a transport error instead of
// hanging on per-ID response channels.
//
// Returns ErrClosed if the transport has been closed.
func (h *HTTPTransport) push(msg JSONRPCMessage) error {
	// Fast path: queue has space.
	select {
	case <-h.done:
		return ErrClosed
	case h.queue <- msg:
		return nil
	default:
	}
	// Queue full — block with overflow cap.
	t := time.NewTimer(h.overflowTimeout)
	defer t.Stop()
	select {
	case <-h.done:
		return ErrClosed
	case h.queue <- msg:
		return nil
	case <-t.C:
		// Demuxer is not draining — fail loud rather than silently
		// drop. Closing the transport unblocks all pending callers
		// via Client.Run's failAll on the next Recv.
		_ = h.Close()
		return fmt.Errorf("%w: response queue full for %s — demuxer not draining",
			ErrTransportUnavailable, h.overflowTimeout)
	}
}

// Recv blocks until a frame is available, ctx is cancelled, or the
// transport is closed. Returns ErrClosed on close — preferred over
// the queue branch so callers get a clear close signal even when
// frames are buffered.
func (h *HTTPTransport) Recv(ctx context.Context) (JSONRPCMessage, error) {
	// Prefer done over queue: when both are ready (transport closed
	// with leftover frames), return ErrClosed.
	select {
	case <-h.done:
		return JSONRPCMessage{}, ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return JSONRPCMessage{}, ctx.Err()
	case <-h.done:
		return JSONRPCMessage{}, ErrClosed
	case msg := <-h.queue:
		return msg, nil
	}
}

// Close releases resources. Idempotent. Closes only h.done — h.queue
// is NOT closed because push may still be blocked on it from another
// goroutine, and sending to a closed channel panics. Recv prefers
// h.done over h.queue so the close is observable immediately.
func (h *HTTPTransport) Close() error {
	h.closeOnce.Do(func() {
		close(h.done)
	})
	return nil
}

// readSummary reads up to 256 bytes from r and returns a single-line
// summary, stripping newlines and truncating. Used for HTTP error
// bodies — we don't want to log the full body (may be huge or carry
// sensitive content) but a short hint is useful for diagnostics.
func readSummary(r io.Reader) string {
	buf, _ := io.ReadAll(io.LimitReader(r, 256))
	s := strings.ReplaceAll(string(buf), "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
