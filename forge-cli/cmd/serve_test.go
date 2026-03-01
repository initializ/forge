package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadDaemonState_NoFile(t *testing.T) {
	_, running := readDaemonState(filepath.Join(t.TempDir(), "serve.json"))
	if running {
		t.Error("expected not running when file does not exist")
	}
}

func TestReadDaemonState_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.json")
	os.WriteFile(path, []byte("not json"), 0644) //nolint:errcheck

	_, running := readDaemonState(path)
	if running {
		t.Error("expected not running for invalid JSON")
	}
}

func TestReadDaemonState_StalePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.json")
	state := daemonState{PID: 999999999, Port: 8080, Host: "127.0.0.1"}
	data, _ := json.Marshal(state)
	os.WriteFile(path, data, 0644) //nolint:errcheck

	_, running := readDaemonState(path)
	if running {
		t.Error("expected not running for stale PID")
	}
}

func TestServeCmd_HasSubcommands(t *testing.T) {
	names := make(map[string]bool)
	for _, sub := range serveCmd.Commands() {
		names[sub.Name()] = true
	}

	for _, want := range []string{"start", "stop", "status", "logs"} {
		if !names[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
}
