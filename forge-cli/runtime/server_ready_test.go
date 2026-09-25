package runtime

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestWaitForServer_ResolvesAutoIncrementedPort proves the fix for the CI flake
// in TestRunner_JSONRPC_WorkflowContextThreadsThroughDispatcher: when the port
// findFreePort handed out is stolen before the runner binds, server.Start
// auto-increments and the server comes up on a higher port. waitForServer must
// discover it by scanning the increment window and return the real URL, rather
// than time out polling the requested port.
func TestWaitForServer_ResolvesAutoIncrementedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// The caller believes the server is one port lower (as if its requested
	// port was taken and Start incremented to actualPort).
	requested := fmt.Sprintf("http://127.0.0.1:%d", actualPort-1)
	got := waitForServer(t, requested, 3*time.Second)
	if want := fmt.Sprintf("http://127.0.0.1:%d", actualPort); got != want {
		t.Errorf("waitForServer resolved %q, want %q (should scan up to the incremented port)", got, want)
	}
}
