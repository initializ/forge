package runtime

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Logger defines the structured logging interface for the runtime.
type Logger interface {
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
	Debug(msg string, fields map[string]any)
}

// JSONLogger writes structured JSON log entries to an io.Writer.
type JSONLogger struct {
	mu      sync.Mutex
	w       io.Writer
	verbose bool
}

// NewJSONLogger creates a JSONLogger writing to w. Debug entries are only
// emitted when verbose is true.
func NewJSONLogger(w io.Writer, verbose bool) *JSONLogger {
	return &JSONLogger{w: w, verbose: verbose}
}

func (l *JSONLogger) Info(msg string, fields map[string]any)  { l.log("info", msg, fields) }
func (l *JSONLogger) Warn(msg string, fields map[string]any)  { l.log("warn", msg, fields) }
func (l *JSONLogger) Error(msg string, fields map[string]any) { l.log("error", msg, fields) }

func (l *JSONLogger) Debug(msg string, fields map[string]any) {
	if !l.verbose {
		return
	}
	l.log("debug", msg, fields)
}

func (l *JSONLogger) log(level, msg string, fields map[string]any) {
	entry := make(map[string]any, len(fields)+3)
	entry["time"] = time.Now().UTC().Format(time.RFC3339)
	entry["level"] = level
	entry["msg"] = msg
	for k, v := range fields {
		entry[k] = v
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	data, _ := json.Marshal(entry)
	data = append(data, '\n')
	l.w.Write(data) //nolint:errcheck
}
