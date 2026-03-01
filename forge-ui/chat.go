package forgeui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// handleChat proxies a chat message to a running agent via A2A JSON-RPC
// and streams the SSE response back to the browser.
func (s *UIServer) handleChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	port, ok := s.pm.GetPort(agentID)
	if !ok {
		writeError(w, http.StatusBadRequest, "agent is not running")
		return
	}

	// Generate session ID if not provided.
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("%s-%d", agentID, time.Now().UnixNano())
	}

	// Build A2A JSON-RPC request for tasks/sendSubscribe.
	rpcBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tasks/sendSubscribe",
		"params": map[string]any{
			"id": sessionID,
			"message": map[string]any{
				"role": "user",
				"parts": []map[string]any{
					{"kind": "text", "text": req.Message},
				},
			},
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build request")
		return
	}

	// POST to the agent's A2A endpoint.
	client := &http.Client{Timeout: 0}
	agentURL := fmt.Sprintf("http://127.0.0.1:%d/", port)
	agentReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, agentURL, bytes.NewReader(rpcBody))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent request")
		return
	}
	agentReq.Header.Set("Content-Type", "application/json")

	agentResp, err := client.Do(agentReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to reach agent: "+err.Error())
		return
	}
	defer func() { _ = agentResp.Body.Close() }()

	// If the agent didn't return SSE, relay the error.
	ct := agentResp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		body, _ := io.ReadAll(agentResp.Body)
		writeError(w, http.StatusBadGateway, "agent returned non-SSE response: "+string(body))
		return
	}

	// Set SSE headers for the browser.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	// Parse agent SSE and re-emit to browser.
	scanner := bufio.NewScanner(agentResp.Body)
	var eventType string
	var dataLines []string

	for scanner.Scan() {
		// Check if client disconnected.
		select {
		case <-r.Context().Done():
			return
		default:
		}

		line := scanner.Text()

		if after, found := strings.CutPrefix(line, "event:"); found {
			eventType = strings.TrimSpace(after)
		} else if after, found := strings.CutPrefix(line, "data:"); found {
			dataLines = append(dataLines, after)
		} else if line == "" && eventType != "" {
			// Blank line = end of SSE frame. Re-emit to browser.
			data := strings.TrimSpace(strings.Join(dataLines, "\n"))
			if data != "" {
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
				flusher.Flush()
			}
			eventType = ""
			dataLines = nil
		}
	}

	// Send final done event with session_id.
	doneData, _ := json.Marshal(map[string]string{"session_id": sessionID})
	_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneData)
	flusher.Flush()
}

// handleListSessions returns stored chat sessions for an agent.
func (s *UIServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	// Find agent directory from scanner.
	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	agent, ok := agents[agentID]
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	sessionsDir := filepath.Join(agent.Directory, ".forge", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		// No sessions directory yet — return empty list.
		writeJSON(w, http.StatusOK, []SessionInfo{})
		return
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// Skip temp files.
		if strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}

		fpath := filepath.Join(sessionsDir, entry.Name())
		raw, err := os.ReadFile(fpath)
		if err != nil {
			continue
		}

		var data struct {
			TaskID    string          `json:"task_id"`
			Messages  json.RawMessage `json:"messages"`
			CreatedAt time.Time       `json:"created_at"`
			UpdatedAt time.Time       `json:"updated_at"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			continue
		}

		// Extract first user message as preview.
		preview := extractPreview(data.Messages)

		sessions = append(sessions, SessionInfo{
			ID:        data.TaskID,
			Preview:   preview,
			CreatedAt: data.CreatedAt,
			UpdatedAt: data.UpdatedAt,
		})
	}

	// Sort newest first.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	if sessions == nil {
		sessions = []SessionInfo{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

// handleGetSession returns the full session data for a specific session.
func (s *UIServer) handleGetSession(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	sid := r.PathValue("sid")
	if agentID == "" || sid == "" {
		writeError(w, http.StatusBadRequest, "agent id and session id are required")
		return
	}

	agents, err := s.scanner.Scan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	agent, ok := agents[agentID]
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	// Sanitize session ID for filesystem safety.
	safeSID := sanitizeForFilename(sid)
	fpath := filepath.Join(agent.Directory, ".forge", "sessions", safeSID+".json")
	raw, err := os.ReadFile(fpath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to read session")
		return
	}

	// Return raw JSON to avoid needing llm.ChatMessage import.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// extractPreview extracts the first user message text from a messages JSON array.
func extractPreview(messagesRaw json.RawMessage) string {
	var messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(messagesRaw, &messages); err != nil || len(messages) == 0 {
		return ""
	}
	for _, m := range messages {
		if m.Role == "user" && m.Content != "" {
			preview := m.Content
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			return preview
		}
	}
	return ""
}

// sanitizeForFilename replaces characters unsafe for filenames.
func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
