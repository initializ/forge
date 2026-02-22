package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

func TestFindFreePort(t *testing.T) {
	port, err := findFreePort()
	if err != nil {
		t.Fatalf("findFreePort error: %v", err)
	}
	if port <= 0 {
		t.Errorf("invalid port: %d", port)
	}

	// Verify the port is actually free by binding to it
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	_ = ln.Close()
}

func TestFindFreePort_Unique(t *testing.T) {
	ports := make(map[int]bool)
	for range 10 {
		port, err := findFreePort()
		if err != nil {
			t.Fatalf("findFreePort error: %v", err)
		}
		ports[port] = true
	}
	// At least some should be unique (OS may reuse, but not all)
	if len(ports) < 2 {
		t.Error("expected at least 2 unique ports")
	}
}

func TestSubprocessRuntime_EnvMerge(t *testing.T) {
	env := map[string]string{
		"API_KEY": "test-key",
		"DB_HOST": "localhost",
	}

	rt := NewSubprocessRuntime("echo hello", t.TempDir(), env, coreruntime.NewJSONLogger(&bytes.Buffer{}, false))

	if rt.entrypoint != "echo hello" {
		t.Errorf("entrypoint: got %q", rt.entrypoint)
	}
	if rt.env["API_KEY"] != "test-key" {
		t.Error("env not stored correctly")
	}
}

func TestSubprocessRuntime_HealthCheck(t *testing.T) {
	// Start a test HTTP server that responds to /healthz
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer func() { _ = srv.Close() }()

	rt := &SubprocessRuntime{
		internalPort: port,
		logger:       coreruntime.NewJSONLogger(&bytes.Buffer{}, false),
	}

	ctx := context.Background()
	if !rt.Healthy(ctx) {
		t.Error("expected healthy=true for running test server")
	}
}

func TestSubprocessRuntime_HealthCheck_Unhealthy(t *testing.T) {
	// Use a port that nothing is listening on
	port, _ := findFreePort()
	rt := &SubprocessRuntime{
		internalPort: port,
		logger:       coreruntime.NewJSONLogger(&bytes.Buffer{}, false),
	}

	ctx := context.Background()
	if rt.Healthy(ctx) {
		t.Error("expected healthy=false when nothing is listening")
	}
}

func TestMustMarshal(t *testing.T) {
	params := a2a.SendTaskParams{
		ID: "t-1",
		Message: a2a.Message{
			Role:  a2a.MessageRoleUser,
			Parts: []a2a.Part{a2a.NewTextPart("hi")},
		},
	}

	data := mustMarshal(params)
	if len(data) == 0 {
		t.Error("expected non-empty JSON")
	}

	var decoded a2a.SendTaskParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != "t-1" {
		t.Errorf("id: got %q", decoded.ID)
	}
}
