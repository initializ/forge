package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// httpSinkName is the stable Name() value used in stats + status
// events. Operators grep "sink=localhost-http" to answer "is the
// localhost HTTP fallback in use?"
const httpSinkName = "localhost-http"

const defaultHTTPWriteTimeout = 50 * time.Millisecond

// httpSink delivers events via HTTP POST to a localhost endpoint. Used
// when the platform deploys to an environment that lacks Unix sockets
// (notably Windows containers, or sandboxed runtimes that forbid
// unix:// dialing). Same fire-and-forget discipline as socketSink: each
// POST has a short timeout, drops on timeout, never blocks the
// emitter, no retry beyond the next event.
//
// The endpoint must be localhost — the issue text mandates it. There is
// no authentication on the wire; trust derives from the localhost
// loopback boundary. Cross-host emission would require a different
// design (TLS, auth, retries) and is explicitly out of scope.
type httpSink struct {
	endpoint string
	timeout  time.Duration
	client   *http.Client

	mu     sync.Mutex
	closed bool

	stats sinkStats
}

// NewHTTPSink constructs a localhost HTTP sink. Returns nil if
// endpoint is empty. The http.Client is built with a per-request
// timeout matching the write timeout; the transport defaults are
// fine — no keep-alive tuning needed because we expect one POST per
// emit and localhost RTT is sub-millisecond.
func NewHTTPSink(endpoint string, writeTimeout time.Duration) Sink {
	if endpoint == "" {
		return nil
	}
	if writeTimeout <= 0 {
		writeTimeout = defaultHTTPWriteTimeout
	}
	return &httpSink{
		endpoint: endpoint,
		timeout:  writeTimeout,
		client: &http.Client{
			Timeout: writeTimeout,
		},
	}
}

func (s *httpSink) Name() string            { return httpSinkName }
func (s *httpSink) Stats() map[string]int64 { return s.stats.snapshot() }

// Write POSTs one NDJSON event. Any non-2xx response counts as a
// dial-class drop (the receiver rejected it; we won't queue). Network
// timeouts count as timeout drops. Like socketSink, returns nil in
// every transient case so the fan-out loop continues.
func (s *httpSink) Write(ctx context.Context, eventBytes []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("audit http sink is closed")
	}
	s.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.endpoint, bytes.NewReader(eventBytes))
	if err != nil {
		s.stats.dropsDial.Add(1)
		return nil
	}
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := s.client.Do(req)
	if err != nil {
		if isTimeoutError(err) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			s.stats.dropsTimeout.Add(1)
		} else {
			s.stats.dropsDial.Add(1)
		}
		return nil
	}
	// Drain + close so the connection can be re-used by the transport.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.stats.dropsDial.Add(1)
		return nil
	}
	s.stats.writesOK.Add(1)
	s.stats.connected.Store(1)
	return nil
}

// Close marks the sink dead. The underlying transport keeps its
// connections; closing it would race with any in-flight POSTs, and
// shutting down a localhost HTTP loop is cheap enough that we don't
// need to be heroic about it. Honors ctx to keep the API consistent
// with the rest of the Sink interface.
func (s *httpSink) Close(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.stats.connected.Store(0)
	return nil
}

// HTTPEndpointForLog is exposed so the runner's startup banner can
// log "exporting audit to <endpoint>" without forcing the runner to
// reach into a private field. Returns the endpoint URL.
func HTTPEndpointForLog(s Sink) string {
	if h, ok := s.(*httpSink); ok {
		return h.endpoint
	}
	return fmt.Sprintf("(non-http sink: %s)", s.Name())
}
