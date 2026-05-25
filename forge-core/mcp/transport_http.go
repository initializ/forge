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
	"sync/atomic"
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
type HTTPTransport struct {
	client     *http.Client
	url        string
	authFn     AuthTokenFunc
	clientInfo string // "<name>/<version>" for User-Agent (best-effort)

	mu        sync.Mutex
	sessionID string

	queue  chan JSONRPCMessage
	closed atomic.Bool
	once   sync.Once
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
		client:     httpClient,
		url:        url,
		authFn:     authFn,
		clientInfo: "forge-mcp/0.12.0",
		queue:      make(chan JSONRPCMessage, 16),
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
	if h.closed.Load() {
		return ErrClosed
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

	// Capture session id if present.
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.mu.Lock()
		h.sessionID = sid
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
	h.push(msg)
	return nil
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
		h.push(msg)
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

// push enqueues a frame, dropping the oldest if the queue is full
// (defensive; the queue is large enough that this should not happen
// under realistic load).
func (h *HTTPTransport) push(msg JSONRPCMessage) {
	if h.closed.Load() {
		return
	}
	select {
	case h.queue <- msg:
	default:
		// Drop oldest to make room — backpressure visible to next Recv
		// caller via slower delivery.
		select {
		case <-h.queue:
		default:
		}
		select {
		case h.queue <- msg:
		default:
		}
	}
}

// Recv blocks until a frame is available, ctx is cancelled, or the
// transport is closed.
func (h *HTTPTransport) Recv(ctx context.Context) (JSONRPCMessage, error) {
	select {
	case <-ctx.Done():
		return JSONRPCMessage{}, ctx.Err()
	case msg, ok := <-h.queue:
		if !ok {
			return JSONRPCMessage{}, ErrClosed
		}
		return msg, nil
	}
}

// Close releases resources. Idempotent.
func (h *HTTPTransport) Close() error {
	h.once.Do(func() {
		h.closed.Store(true)
		close(h.queue)
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
