package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// TestAuditExport_EndToEnd mirrors the runner-side wiring: build an
// AuditLogger via NewAuditLoggerFromConfig (the exact path
// forge-cli/runtime/runner.go uses when --audit-socket is set), emit a
// realistic mix of events, and confirm every event lands on BOTH the
// safety-net writer AND the UDS sink in identical bytes.
//
// This is the issue-spec integration test: §"Integration test" in
// FWS-7's testing requirements (issue #95). The spec asks for a forge
// agent process and an A2A client; we cover the same contract more
// cheaply by exercising the construction + fan-out path the runner
// runs. Full process-level coverage is in scripts/manual-test-fws-7.sh.
func TestAuditExport_EndToEnd(t *testing.T) {
	if !canBindUnix() {
		t.Skip("unix domain sockets unavailable on this platform")
	}
	sockPath := shortSocketPath(t, "audit.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type lineMsg struct {
		evt   AuditEvent
		bytes []byte
	}
	const expected = 4
	got := make(chan lineMsg, expected)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		for i := 0; i < expected; i++ {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var evt AuditEvent
			_ = json.Unmarshal(bytes.TrimSpace(line), &evt)
			got <- lineMsg{evt: evt, bytes: line}
		}
	}()

	// Stand in a buffer for stderr so we can inspect the safety-net
	// stream side-by-side. NewAuditLoggerFromConfig hard-codes stderr;
	// we drop a buffer sink in front via AddSink + drop the stderr
	// one by surgery on the slice. (In production the stderr sink
	// always lives at sinks[0]; this is a test workaround so we
	// don't capture the test runner's terminal.)
	var stderrBuf bytes.Buffer
	logger := NewAuditLoggerFromConfig(AuditExportConfig{
		SocketPath:   sockPath,
		WriteTimeout: 500 * time.Millisecond,
		DialTimeout:  500 * time.Millisecond,
	})
	logger.mu.Lock()
	logger.sinks = []Sink{newWriterSink(&stderrBuf, "buf-as-stderr"), logger.sinks[1]}
	logger.mu.Unlock()

	// Emit the kind of mixed event sequence a real agent startup
	// produces: policy_loaded, agent_card_published, an in-request
	// llm_call, and the periodic audit_export_status.
	logger.Emit(AuditEvent{Event: AuditPolicyLoaded,
		Fields: map[string]any{"layer": "system", "source": "/etc/forge/policy.yaml"}})
	logger.Emit(AuditEvent{Event: EventAgentCardPublished,
		Fields: map[string]any{"name": "demo-agent", "version": "0.1.0", "skill_count": 3}})
	logger.EmitLLMCall(context.Background(), LLMCallAuditArgs{
		Model: "gpt-4o", Provider: "openai", Duration: 250 * time.Millisecond,
		Usage: LLMUsage{InputTokens: 120, OutputTokens: 45},
	})
	emitAuditExportStatus(logger, auditStatusReasonKeepalive)

	// Receive on the UDS side and assert per-event parity with the
	// stderr-side capture.
	type lineSnap struct {
		event string
		body  []byte
	}
	var udsSeen []lineSnap
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
collect:
	for i := 0; i < expected; i++ {
		select {
		case msg := <-got:
			udsSeen = append(udsSeen, lineSnap{event: msg.evt.Event, body: msg.bytes})
		case <-deadline.C:
			break collect
		}
	}
	if len(udsSeen) != expected {
		t.Fatalf("UDS received %d events, want %d (stderr buf was:\n%s)", len(udsSeen), expected, stderrBuf.String())
	}

	// Both sinks should carry the same byte payloads in the same
	// order — the AuditLogger fan-out is sequential and the
	// serializer runs once per event.
	stderrLines := strings.Split(strings.TrimRight(stderrBuf.String(), "\n"), "\n")
	if len(stderrLines) != expected {
		t.Fatalf("stderr received %d lines, want %d:\n%s", len(stderrLines), expected, stderrBuf.String())
	}
	for i, line := range stderrLines {
		want := strings.TrimRight(string(udsSeen[i].body), "\n")
		if line != want {
			t.Errorf("byte parity broken at line %d:\n  stderr: %q\n  uds:    %q", i, line, want)
		}
	}

	// Spot-check that the event types arrived in the order emitted.
	wantOrder := []string{AuditPolicyLoaded, EventAgentCardPublished, AuditLLMCall, AuditExportStatus}
	for i, want := range wantOrder {
		if udsSeen[i].event != want {
			t.Errorf("event[%d] = %q, want %q", i, udsSeen[i].event, want)
		}
	}
}

// canBindUnix probes whether the OS supports binding to a Unix
// Domain Socket. Skips the test on platforms (Windows containers,
// sandboxed CI) where unix:// dialing returns "invalid argument".
func canBindUnix() bool {
	// MkdirTemp under /tmp keeps the path short for macOS's UDS
	// length limit; the probe immediately closes and removes.
	ln, err := net.Listen("unix", "/tmp/forge-fws7-probe.sock")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
