package forgeui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "1.1.0", true},
		{"1.0.0", "2.0.0", true},
		{"1.0.1", "1.0.0", false},
		{"1.1.0", "1.0.0", false},
		{"2.0.0", "1.0.0", false},
		{"1.0.0", "1.0.0", false},
		{"0.9.0", "1.0.0", true},
		{"1.2.3", "1.2.4", true},
		{"1.2.3", "1.3.0", true},
	}

	for _, tc := range tests {
		got := isNewer(tc.current, tc.latest)
		if got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	tests := []struct {
		input string
		want  [3]int
	}{
		{"1.2.3", [3]int{1, 2, 3}},
		{"v1.2.3", [3]int{1, 2, 3}},
		{"0.1.0", [3]int{0, 1, 0}},
		{"2.0.0-rc1", [3]int{2, 0, 0}},
		{"1.5", [3]int{1, 5, 0}},
		{"3", [3]int{3, 0, 0}},
		{"dev", [3]int{0, 0, 0}},
	}

	for _, tc := range tests {
		got := parseSemver(tc.input)
		if got != tc.want {
			t.Errorf("parseSemver(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestHandleUpdateCheckDevVersion(t *testing.T) {
	srv := NewUIServer(UIServerConfig{
		Port:      4200,
		WorkDir:   t.TempDir(),
		ExePath:   "/usr/bin/false",
		Version:   "dev",
		AgentPort: 9100,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/update-check", nil)
	w := httptest.NewRecorder()
	srv.handleUpdateCheck(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if resp["has_update"] != false {
		t.Errorf("expected has_update=false for dev version, got %v", resp["has_update"])
	}
}

func TestUpdateCheckerCachedResult(t *testing.T) {
	uc := newUpdateChecker("1.0.0", "initializ", "forge")

	// Pre-populate cached state to avoid hitting GitHub.
	uc.mu.Lock()
	uc.latest = "2.0.0"
	uc.hasUpdate = true
	uc.checkedAt = time.Now()
	uc.mu.Unlock()

	latest, hasUpdate, errMsg := uc.check()
	if errMsg != "" {
		t.Errorf("unexpected error: %s", errMsg)
	}
	if !hasUpdate {
		t.Error("expected has_update=true from cache")
	}
	if latest != "2.0.0" {
		t.Errorf("expected latest=2.0.0, got %s", latest)
	}
}
