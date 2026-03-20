package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// AuditEvent represents an audit log entry in NDJSON format.
type AuditEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"` // "allowed", "denied", "exit"
	Host      string    `json:"host,omitempty"`
	Port      int       `json:"port,omitempty"`
	PID       int       `json:"pid,omitempty"`
	ExitCode  int       `json:"exit_code,omitempty"`
}

// AuditLogger writes NDJSON audit events to stdout.
type AuditLogger struct {
	mu sync.Mutex
}

// NewAuditLogger creates a new AuditLogger.
func NewAuditLogger() *AuditLogger {
	return &AuditLogger{}
}

// Log writes an audit event to stdout in NDJSON format.
func (a *AuditLogger) Log(event *AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("ERROR: marshal audit event: %v", err)
		return
	}

	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}

// LogExitEvent logs an agent exit event.
func (a *AuditLogger) LogExitEvent(pid, exitCode int) {
	a.Log(&AuditEvent{
		Timestamp: time.Now().UTC(),
		Action:    "exit",
		PID:       pid,
		ExitCode:  exitCode,
	})
}
