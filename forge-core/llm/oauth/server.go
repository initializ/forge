package oauth

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"
	"time"
)

// CallbackResult holds the result from the OAuth callback.
type CallbackResult struct {
	Code  string
	State string
	Error string
}

// CallbackServer is a local HTTP server that receives the OAuth authorization code.
type CallbackServer struct {
	addr     string // loopback listen address, "host:port"
	path     string // callback path, e.g. /auth/callback
	resultCh chan CallbackResult
	server   *http.Server
	mu       sync.Mutex
}

// NewCallbackServer creates a callback server bound to addr ("host:port") with
// the callback handler mounted at path. Both are derived from the flow's
// redirect_uri so the listener matches what the IdP redirects to (#490 — a
// configurable loopback, not a fixed port).
func NewCallbackServer(addr, path string) *CallbackServer {
	if path == "" {
		path = "/"
	}
	return &CallbackServer{
		addr:     addr,
		path:     path,
		resultCh: make(chan CallbackResult, 1),
	}
}

// Start starts the callback server and returns immediately.
func (s *CallbackServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.handleCallback)

	s.mu.Lock()
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.mu.Unlock()

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("starting callback server on %s: %w", s.addr, err)
	}

	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.resultCh <- CallbackResult{Error: err.Error()}
		}
	}()

	return nil
}

// WaitForCode blocks until an authorization code is received or the context expires.
func (s *CallbackServer) WaitForCode(ctx context.Context) (CallbackResult, error) {
	select {
	case result := <-s.resultCh:
		if result.Error != "" {
			return result, fmt.Errorf("oauth callback error: %s", result.Error)
		}
		return result, nil
	case <-ctx.Done():
		return CallbackResult{}, fmt.Errorf("timed out waiting for authorization")
	}
}

// Stop shuts down the callback server.
func (s *CallbackServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		_ = s.server.Close()
	}
}

func (s *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	if errMsg := query.Get("error"); errMsg != "" {
		desc := query.Get("error_description")
		s.resultCh <- CallbackResult{Error: fmt.Sprintf("%s: %s", errMsg, desc)}
		// desc is an untrusted OAuth query param reflected into the page; escape
		// it so a crafted error_description can't inject markup/script into this
		// (loopback) callback page.
		_, _ = fmt.Fprintf(w, "<html><body><h1>Authorization Failed</h1><p>%s</p><p>You can close this tab.</p></body></html>", html.EscapeString(desc))
		return
	}

	code := query.Get("code")
	state := query.Get("state")

	if code == "" {
		s.resultCh <- CallbackResult{Error: "no code in callback"}
		_, _ = fmt.Fprint(w, "<html><body><h1>Error</h1><p>No authorization code received.</p></body></html>")
		return
	}

	s.resultCh <- CallbackResult{Code: code, State: state}
	_, _ = fmt.Fprint(w, "<html><body><h1>Authorization Successful</h1><p>You can close this tab and return to the terminal.</p></body></html>")
}
