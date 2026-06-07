package runtime

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	gort "runtime"
	"testing"
	"time"
)

// Benchmarks for FWS-7 (issue #95) sink overhead. The acceptance
// criterion is < 20% overhead when both stderr + socket sinks are
// configured (vs. stderr only), and < 2x overhead when the socket is
// unreachable (cached failure state keeps the dial path fast).
//
// Run:
//   go test -bench=BenchmarkEmit -run=^$ ./forge-core/runtime

// shortBenchSocketPath mirrors shortSocketPath but uses os.MkdirTemp
// directly so it can be called from benchmark code (b.Helper not
// always available in old Go versions; explicit cleanup is cheaper).
func shortBenchSocketPath(b *testing.B, name string) string {
	b.Helper()
	dir, err := os.MkdirTemp("/tmp", "f7b")
	if err != nil {
		b.Fatalf("mkdir tmp: %v", err)
	}
	b.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// startBenchListener accepts on a Unix socket and drains everything to
// /dev/null. Returns the cleanup that closes the listener.
func startBenchListener(b *testing.B, path string) func() {
	b.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(io.Discard, bufio.NewReader(c))
			}(c)
		}
	}()
	return func() {
		_ = ln.Close()
		<-done
	}
}

// BenchmarkEmit_StderrOnly is the baseline. Single writer sink to
// io.Discard so we benchmark the audit pipeline, not stderr's
// terminal buffering.
func BenchmarkEmit_StderrOnly(b *testing.B) {
	logger := &AuditLogger{
		sinks:   []Sink{newWriterSink(io.Discard, "discard")},
		logOnce: map[string]bool{},
	}
	evt := AuditEvent{Event: "bench", Fields: map[string]any{"k": "v"}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Emit(evt)
	}
}

// BenchmarkEmit_StderrAndSocket measures the dual-sink overhead with
// a live UDS peer that's draining as fast as we write. Target: < 20%
// overhead vs. the stderr-only baseline.
func BenchmarkEmit_StderrAndSocket(b *testing.B) {
	if gort.GOOS == "windows" {
		b.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortBenchSocketPath(b, "audit.sock")
	stop := startBenchListener(b, sockPath)
	defer stop()

	logger := &AuditLogger{
		sinks: []Sink{
			newWriterSink(io.Discard, "discard"),
			NewSocketSink(sockPath, 500*time.Millisecond, 500*time.Millisecond),
		},
		logOnce: map[string]bool{},
	}
	evt := AuditEvent{Event: "bench", Fields: map[string]any{"k": "v"}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Emit(evt)
	}
}

// BenchmarkEmit_SocketUnreachable is the resilience benchmark: the
// socket sink points at a path with no listener. The cached
// backoff-state should keep the emit path fast despite every event
// going to drop. Target: < 2x overhead vs. stderr-only.
func BenchmarkEmit_SocketUnreachable(b *testing.B) {
	if gort.GOOS == "windows" {
		b.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortBenchSocketPath(b, "absent.sock")
	logger := &AuditLogger{
		sinks: []Sink{
			newWriterSink(io.Discard, "discard"),
			NewSocketSink(sockPath, 50*time.Millisecond, 50*time.Millisecond),
		},
		logOnce: map[string]bool{},
	}
	evt := AuditEvent{Event: "bench"}
	// Warm the sink's cached-failure state so the benchmarked loop
	// measures steady-state drop-fast behavior, not the one-time
	// initial dial cost.
	for i := 0; i < 5; i++ {
		logger.Emit(evt)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Emit(evt)
	}
}
