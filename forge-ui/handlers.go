package forgeui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleListAgents returns all discovered agents merged with process state.
func (s *UIServer) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pm.MergeState(agents)

	// Convert to sorted slice
	list := make([]*AgentInfo, 0, len(agents))
	for _, a := range agents {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})

	writeJSON(w, http.StatusOK, list)
}

// handleGetAgent returns a single agent by ID.
func (s *UIServer) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pm.MergeState(agents)

	agent, ok := agents[id]
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	writeJSON(w, http.StatusOK, agent)
}

// handleStartAgent starts an agent process. Accepts optional JSON body with passphrase.
func (s *UIServer) handleStartAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	// Parse optional request body for passphrase.
	var req StartRequest
	if r.Body != nil && r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	agent, ok := agents[id]
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	// If agent needs a passphrase and one was provided, set it in the environment
	// so OverlaySecretsToEnv can decrypt secrets.enc.
	if req.Passphrase != "" {
		_ = os.Setenv("FORGE_PASSPHRASE", req.Passphrase)
	} else if agent.NeedsPassphrase && os.Getenv("FORGE_PASSPHRASE") == "" {
		writeError(w, http.StatusBadRequest, "passphrase required for encrypted secrets")
		return
	}

	if err := s.pm.Start(id, agent); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "starting", "agent_id": id})
}

// handleStopAgent stops an agent process.
func (s *UIServer) handleStopAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	if err := s.pm.Stop(id); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping", "agent_id": id})
}

// handleRescan forces a workspace re-scan and returns the updated agent list.
func (s *UIServer) handleRescan(w http.ResponseWriter, r *http.Request) {
	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pm.MergeState(agents)

	list := make([]*AgentInfo, 0, len(agents))
	for _, a := range agents {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})

	writeJSON(w, http.StatusOK, list)
}

// handleSSE streams real-time events to the client.
func (s *UIServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ch := s.broker.Subscribe()
	defer s.broker.Unsubscribe(ch)

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event.Data)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleHealth returns a health check response.
func (s *UIServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	running := 0
	agents, _ := s.scanner.Scan()
	s.pm.MergeState(agents)
	for _, a := range agents {
		if a.Status == StateRunning || a.Status == StateStarting {
			running++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"agents_running": running,
	})
}
