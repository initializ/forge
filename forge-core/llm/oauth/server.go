package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// CallbackResult holds the result from the OAuth callback.
type CallbackResult struct {
	Code  string
	State string
	Error string
}

// CallbackServer is a local HTTP server that receives the OAuth authorization code.
type CallbackServer struct {
	port     int
	resultCh chan CallbackResult
	server   *http.Server
	mu       sync.Mutex
}

// NewCallbackServer creates a callback server on the given port.
func NewCallbackServer(port int) *CallbackServer {
	return &CallbackServer{
		port:     port,
		resultCh: make(chan CallbackResult, 1),
	}
}

// Start starts the callback server and returns immediately.
func (s *CallbackServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", s.handleCallback)

	s.mu.Lock()
	s.server = &http.Server{
		Handler: mux,
	}
	s.mu.Unlock()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("starting callback server on port %d: %w", s.port, err)
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
		_, _ = fmt.Fprintf(w, "<html><body><h1>Authorization Failed</h1><p>%s</p><p>You can close this tab.</p></body></html>", desc)
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
