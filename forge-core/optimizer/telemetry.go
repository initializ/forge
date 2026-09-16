package optimizer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Report is the per-request record handed to a Reporter after each proxied
// call. In phase 1 the token figures are the provider's real billed counts;
// phase 2 will add compression savings alongside them.
type Report struct {
	CorrelationID string `json:"correlation_id"`
	SessionID     string `json:"session_id,omitempty"`
	Client        string `json:"client,omitempty"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	StatusCode    int    `json:"status_code"`
	Streaming     bool   `json:"streaming"`
	Usage         Usage  `json:"usage"`
	// Compression is this request's savings when the optimizer compressed the
	// outbound body. SavedTokens is a tokenizer estimate; Usage above carries
	// the provider's real billed counts after compression.
	Compression CompressStats `json:"compression"`
	// RecalledEpisodes is how many past episodes the optimizer injected into
	// this request's system prompt (memory recall, see memory_recall.go).
	RecalledEpisodes int    `json:"recalled_episodes,omitempty"`
	Upstream         string `json:"upstream"`
	DurationMS       int64  `json:"duration_ms"`
	Error            string `json:"error,omitempty"`
}

// Reporter receives one Report per proxied request. Implementations must be
// non-blocking and safe for concurrent use — the proxy path calls Report
// inline after streaming the response.
type Reporter interface {
	Report(ctx context.Context, r Report)
}

// NopReporter discards reports.
type NopReporter struct{}

// Report implements Reporter.
func (NopReporter) Report(context.Context, Report) {}

// LogReporter emits each report as a structured log line. This is the phase-1
// default so token usage is visible immediately without a control plane.
type LogReporter struct{ Logger *slog.Logger }

// Report implements Reporter.
func (l LogReporter) Report(_ context.Context, r Report) {
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"cid", r.CorrelationID,
		"session", r.SessionID,
		"path", r.Path,
		"status", r.StatusCode,
		"streaming", r.Streaming,
		"model", r.Usage.Model,
		"input_tokens", r.Usage.InputTokens,
		"output_tokens", r.Usage.OutputTokens,
		"cache_read_tokens", r.Usage.CacheReadInputTokens,
		"cache_creation_tokens", r.Usage.CacheCreationInputTokens,
		"compression_saved_tokens", r.Compression.SavedTokens,
		"compressed_blocks", r.Compression.Blocks,
		"duration_ms", r.DurationMS,
	}
	if r.Error != "" {
		attrs = append(attrs, "error", r.Error)
		logger.Error("optimizer request", attrs...)
		return
	}
	logger.Info("optimizer request", attrs...)
}

// ControlPlaneReporter POSTs each report to the initializ control plane. It
// follows the platform tenancy contract: Org-Id + Workspace-Id headers
// alongside the per-org bearer token, which the platform needs to pick the
// signing secret BEFORE it can validate the bearer.
//
// Delivery is fire-and-forget with a short timeout — metering must never slow
// or fail the proxied request. A dropped report is a lost metric, not a failed
// call.
type ControlPlaneReporter struct {
	Endpoint    string
	OrgID       string
	WorkspaceID string
	Token       string
	Client      *http.Client
	Logger      *slog.Logger
}

// Report implements Reporter.
func (c ControlPlaneReporter) Report(ctx context.Context, r Report) {
	if c.Endpoint == "" {
		return
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}

	body, err := json.Marshal(r)
	if err != nil {
		return
	}

	// Detach from the request context so reporting outlives the response, but
	// keep a hard cap so a stuck control plane can't leak goroutines.
	go func() {
		reqCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if c.OrgID != "" {
			req.Header.Set("Org-Id", c.OrgID)
		}
		if c.WorkspaceID != "" {
			req.Header.Set("Workspace-Id", c.WorkspaceID)
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		// Session id travels both in the JSON body and as a header, so the
		// platform can attribute usage per session (alongside the Org-Id /
		// Workspace-Id tenancy headers) without parsing the payload.
		if r.SessionID != "" {
			req.Header.Set(SessionHeader, r.SessionID)
		}
		resp, err := client.Do(req)
		if err != nil {
			if c.Logger != nil {
				c.Logger.Debug("optimizer telemetry drop", "error", err.Error())
			}
			return
		}
		_ = resp.Body.Close()
	}()
}

// MultiReporter fans a report out to several reporters (e.g. log + control
// plane).
type MultiReporter []Reporter

// Report implements Reporter.
func (m MultiReporter) Report(ctx context.Context, r Report) {
	for _, rep := range m {
		if rep != nil {
			rep.Report(ctx, r)
		}
	}
}
