package forgeui

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/optimizer"
)

// optimizerBaseURL is where the local forge optimizer listens. Overridable so
// the dashboard can point at a shared / non-default-port optimizer.
func optimizerBaseURL() string {
	if v := os.Getenv("FORGE_OPTIMIZER_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:8787"
}

// handleOptimizerStats proxies the running optimizer's /stats so the dashboard
// can show coding-agent session data (tokens, compression savings, per session
// and per model). If no optimizer is running it returns available:false rather
// than an error, so the UI can render a friendly "not running" state.
func (s *UIServer) handleOptimizerStats(w http.ResponseWriter, r *http.Request) {
	base := optimizerBaseURL()
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/stats")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "base_url": base})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "base_url": base})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"base_url":  base,
		"stats":     json.RawMessage(body),
	})
}

// optimizerForgeHome is the shared ~/.forge directory the optimizer writes to
// (fallback ./.forge). Kept in sync with the CLI so the dashboard reads the
// same files regardless of which directory a session ran from.
func optimizerForgeHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".forge")
	}
	return ".forge"
}

// optimizerUsageLogPath resolves the durable usage log: env override, else
// ~/.forge/optimizer-usage.jsonl (shared across sessions).
func (s *UIServer) optimizerUsageLogPath() string {
	if v := os.Getenv("FORGE_OPTIMIZER_USAGE_LOG"); v != "" {
		return v
	}
	return filepath.Join(optimizerForgeHome(), "optimizer-usage.jsonl")
}

// optimizerMemoryStorePath resolves the local episodic memory store: env
// override, else ~/.forge/optimizer-memory.jsonl (shared across sessions).
func (s *UIServer) optimizerMemoryStorePath() string {
	if v := os.Getenv("FORGE_OPTIMIZER_MEMORY_STORE"); v != "" {
		return v
	}
	return filepath.Join(optimizerForgeHome(), "optimizer-memory.jsonl")
}

// handleOptimizerMemory reads the local episodic memory store and returns the
// distilled episodes (optionally filtered by ?repo=), most recent first, so the
// dashboard can show what the optimizer has learned from coding sessions. This
// is the LOCAL (OSS) view; when a control-plane URL is configured, memory also
// flows there — but the dashboard reads the local write-through copy.
func (s *UIServer) handleOptimizerMemory(w http.ResponseWriter, r *http.Request) {
	path := s.optimizerMemoryStorePath()
	store, err := optimizer.NewFileMemoryStore(path)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "store_path": path, "error": err.Error()})
		return
	}
	defer func() { _ = store.Close() }()
	eps, err := store.ListEpisodes(r.URL.Query().Get("repo"), 500)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "store_path": path, "error": err.Error()})
		return
	}
	// Attach per-episode recall usage ("injected into N sessions") and effective
	// confidence (prior + feedback) so the dashboard shows which memories are used
	// and how trusted they are.
	counts, _ := store.RecallCounts()
	fb, _ := store.FeedbackAgg()
	recalled := 0
	for i := range eps {
		if st, ok := counts[eps[i].ID]; ok {
			eps[i].RecallCount = st.Count
			eps[i].LastRecalled = st.LastRecalled
			recalled++
		}
		eps[i].EffectiveConfidence = optimizer.EffectiveConfidence(eps[i].Confidence, fb[eps[i].ID])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":      len(eps) > 0,
		"store_path":     path,
		"count":          len(eps),
		"recalled_count": recalled,
		"episodes":       eps,
	})
}

// handleOptimizerMemoryDelete removes one episode by id (?id=…) from the local
// store, so the dashboard's delete action can prune a bad or stale memory.
func (s *UIServer) handleOptimizerMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing id"})
		return
	}
	path := s.optimizerMemoryStorePath()
	store, err := optimizer.NewFileMemoryStore(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer func() { _ = store.Close() }()
	deleted, err := store.DeleteEpisode(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "id": id})
}

// handleOptimizerMemoryFeedback records an explicit 👍/👎 for a memory
// (?id=…&signal=up|down), moving its effective confidence.
func (s *UIServer) handleOptimizerMemoryFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	signal := r.URL.Query().Get("signal")
	if id == "" || (signal != optimizer.SignalUp && signal != optimizer.SignalDown) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id and signal=up|down required"})
		return
	}
	store, err := optimizer.NewFileMemoryStore(s.optimizerMemoryStorePath())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer func() { _ = store.Close() }()
	if err := store.RecordFeedback(id, signal, optimizer.HumanFeedbackWeight, signal == optimizer.SignalUp); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "signal": signal})
}

// claudeSettingsBaseURL reads ANTHROPIC_BASE_URL from ~/.claude/settings.json's
// env block ("" if unset) — so the dashboard can show whether the daemon has
// wired Claude Code to route through the optimizer.
func claudeSettingsBaseURL() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(h, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	return m.Env["ANTHROPIC_BASE_URL"]
}

// handleOptimizerDaemon reports whether the background optimizer is running and
// whether Claude Code is wired to it — for the dashboard's status banner.
func (s *UIServer) handleOptimizerDaemon(w http.ResponseWriter, r *http.Request) {
	base := optimizerBaseURL()
	running := false
	client := &http.Client{Timeout: 800 * time.Millisecond}
	if resp, err := client.Get(strings.TrimRight(base, "/") + "/healthz"); err == nil {
		running = resp.StatusCode == http.StatusOK
		_ = resp.Body.Close()
	}
	_, stErr := os.Stat(filepath.Join(optimizerForgeHome(), "optimizer-daemon.json"))
	writeJSON(w, http.StatusOK, map[string]any{
		"running":        running,
		"base_url":       base,
		"wired_base_url": claudeSettingsBaseURL(),
		"managed":        stErr == nil, // started via `forge optimizer start`
	})
}

// handleOptimizerDaemonControl runs `forge optimizer <start|stop>` via the forge
// binary the dashboard was launched with, so the UI Start/Stop button performs
// the exact same settings-merge/revert + MCP wiring as the CLI.
func (s *UIServer) handleOptimizerDaemonControl(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		exe := s.cfg.ExePath
		if exe == "" {
			exe = "forge"
		}
		out, err := exec.Command(exe, "optimizer", action).CombinedOutput() //nolint:gosec // fixed subcommand
		resp := map[string]any{"ok": err == nil, "action": action, "output": strings.TrimSpace(string(out))}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleOptimizerSavings reads the durable usage log and returns the rolled-up
// savings (windowed dollars + per-session history), so the dashboard can show
// PAST sessions that the in-memory /stats no longer holds. Uses negotiated
// rates from .forge/optimizer-pricing.json when present.
func (s *UIServer) handleOptimizerSavings(w http.ResponseWriter, r *http.Request) {
	logPath := s.optimizerUsageLogPath()
	overrides, _ := optimizer.LoadPricingFile(filepath.Join(optimizerForgeHome(), "optimizer-pricing.json"))
	pricing := optimizer.NewPricing(overrides)

	report, err := optimizer.AggregateUsageLog(logPath, pricing, time.Now(), 500)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "log_path": logPath, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": report.Records > 0,
		"log_path":  logPath,
		"report":    report,
	})
}
