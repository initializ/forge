package optimizer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Ambient (non-participatory) memory formation for coding agents. The optimizer
// watches the message history flow past on the wire and, when a task boundary is
// crossed, distills the just-completed span into an Episode via a small off-turn
// LLM call — reusing the SAME upstream + credentials from the triggering request
// so it costs the caller nothing extra to wire up. This is the §12.8 "wire
// adapter": bindings (task start/end, outcome) are INFERRED, so records are
// stamped binding="inferred" and source_kind="agent_inference".
//
// Formation is best-effort and fully asynchronous: it never blocks or fails the
// proxied request.

// Distiller turns a rendered transcript span into a structured Episode, and a
// cluster of episodes into a generalized procedure.
type Distiller interface {
	Distill(ctx context.Context, in DistillInput) (*Episode, error)
	// DistillProcedure generalizes a cluster of related episodes into one
	// procedural record (kind=procedural). The returned Episode carries the
	// content only (Summary/Actions=steps/Errors=pitfalls/Lesson/Entities); the
	// caller stamps id, repo, about, episode_ids, and timestamps.
	DistillProcedure(ctx context.Context, in ProcedureInput) (*Episode, error)
}

// ProcedureInput is a cluster of episodes to generalize plus the endpoint/creds.
type ProcedureInput struct {
	Episodes []Episode
	Model    string
	Upstream string
	Headers  http.Header
	Repo     string
}

// DistillInput carries the completed task span plus the credentials/endpoint to
// reach the model, and the metadata to stamp on the resulting episode.
type DistillInput struct {
	Transcript string      // rendered span (roles, text, tool calls, truncated results)
	Model      string      // model to distill with (the request's own model)
	Upstream   string      // upstream base URL (e.g. https://api.anthropic.com)
	Headers    http.Header // auth + anthropic-version/beta, copied from the request
	Repo       string
	Commit     string
	SessionID  string
	Client     string
}

// maxTranscriptChars bounds what we send to the distiller — the point of the
// optimizer is to NOT ship huge context, and errors/decisions cluster at the
// head and tail of a task span.
const maxTranscriptChars = 12000

// distillSystemPrompt asks for a compact, code-grounded episode as JSON.
const distillSystemPrompt = `You are a memory distiller for a coding agent. You are given the transcript of ONE completed unit of work (a "task span"). Extract a compact, reusable episodic memory.

Return ONLY a JSON object, no prose, with these fields:
- "task_signature": a short stable phrase naming the task (e.g. "add retry to http client"). Lowercase, no punctuation.
- "summary": 1-2 sentences on what was attempted and how.
- "actions": array of short strings, the key concrete steps taken (max 6).
- "outcome": one of "success", "failure", "abandoned", "unknown".
- "lesson": one sentence a future agent should remember for a similar task (may be empty).
- "files": array of file paths touched or referenced (max 10).
- "errors": array of short error signatures encountered (max 6, may be empty).

Be terse. Omit anything you are unsure of rather than guessing.`

// claudeCodeIdentity is the exact system-prompt line Anthropic requires on the
// first system block for requests authenticated with a Claude Code subscription
// OAuth token. Claude Code OAuth tokens are scoped to this identity, so a
// side-call that reuses the token (as the distiller does) must carry it as its
// system prompt or the API rejects it with 401. Harmless for API-key auth. The
// actual distillation instructions go in the user turn instead.
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// httpDistiller calls an Anthropic-compatible /v1/messages endpoint.
type httpDistiller struct {
	client *http.Client
}

// NewHTTPDistiller builds a distiller that posts to the request's own upstream.
func NewHTTPDistiller() *httpDistiller {
	return &httpDistiller{client: &http.Client{Timeout: 60 * time.Second}}
}

func (d *httpDistiller) Distill(ctx context.Context, in DistillInput) (*Episode, error) {
	transcript := in.Transcript
	if len(transcript) > maxTranscriptChars {
		// keep head + tail
		head := transcript[:maxTranscriptChars*2/3]
		tail := transcript[len(transcript)-maxTranscriptChars/3:]
		transcript = head + "\n...[span truncated]...\n" + tail
	}
	text, err := d.callMessages(ctx, in.Headers, in.Upstream, in.Model, 700,
		distillSystemPrompt+"\n\nTask span transcript:\n\n"+transcript)
	if err != nil {
		return nil, err
	}
	return parseEpisodeJSON(text)
}

// callMessages posts a single non-streaming Messages request carrying the Claude
// Code identity as the system prompt (OAuth scope requirement) and the given
// instruction as the user turn, reusing the caller's credentials. Returns the
// concatenated text of the response.
func (d *httpDistiller) callMessages(ctx context.Context, headers http.Header, upstream, model string, maxTokens int, userText string) (string, error) {
	reqBody := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system": []map[string]any{
			{"type": "text", "text": claudeCodeIdentity},
		},
		"messages": []map[string]any{
			{"role": "user", "content": userText},
		},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(upstream, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	for _, h := range []string{"authorization", "x-api-key", "anthropic-version", "anthropic-beta"} {
		if v := headers.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("distiller upstream status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return extractMessageText(body)
}

// DistillProcedure generalizes a cluster of episodes into one procedural record.
func (d *httpDistiller) DistillProcedure(ctx context.Context, in ProcedureInput) (*Episode, error) {
	text, err := d.callMessages(ctx, in.Headers, in.Upstream, in.Model, 2000,
		consolidateSystemPrompt+"\n\nEpisodes in this repo to generalize:\n\n"+renderEpisodesForConsolidation(in.Episodes))
	if err != nil {
		return nil, err
	}
	return parseProcedureJSON(text)
}

// extractMessageText pulls the concatenated text blocks from a non-streaming
// Messages API response.
func extractMessageText(body []byte) (string, error) {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), nil
}

// extractJSONObject pulls a JSON object out of a model response that may be
// wrapped in a ```json fence or surrounded by prose. Strips fences first, then
// takes the first '{' to the last '}'.
func extractJSONObject(text string) string {
	s := strings.TrimSpace(text)
	// Strip a leading ```json / ``` fence and any trailing fence.
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			s = s[i : j+1]
		}
	}
	return s
}

// parseEpisodeJSON tolerantly parses the distiller's JSON output (handles a
// stray ```json fence or surrounding prose by scanning for the object).
func parseEpisodeJSON(text string) (*Episode, error) {
	s := extractJSONObject(text)
	var parsed struct {
		TaskSignature string   `json:"task_signature"`
		Summary       string   `json:"summary"`
		Actions       []string `json:"actions"`
		Outcome       string   `json:"outcome"`
		Lesson        string   `json:"lesson"`
		Files         []string `json:"files"`
		Errors        []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, fmt.Errorf("parsing distilled episode: %w", err)
	}
	if parsed.Summary == "" && parsed.TaskSignature == "" {
		return nil, fmt.Errorf("distilled episode is empty")
	}
	outcome := strings.ToLower(strings.TrimSpace(parsed.Outcome))
	switch outcome {
	case OutcomeSuccess, OutcomeFailure, OutcomeAbandoned:
	default:
		outcome = OutcomeUnknown
	}
	return &Episode{
		TaskSignature: parsed.TaskSignature,
		Summary:       parsed.Summary,
		Actions:       parsed.Actions,
		Outcome:       outcome,
		Lesson:        parsed.Lesson,
		Files:         parsed.Files,
		Errors:        parsed.Errors,
	}, nil
}

// MemoryFormer observes request bodies per session, detects task boundaries, and
// asynchronously distills+stores completed spans.
type MemoryFormer struct {
	store        MemoryStore
	distiller    Distiller
	repo         string
	commit       string
	repoResolver func(cwd string) (repo, commit string)
	logger       *slog.Logger

	sem chan struct{} // bounds concurrent distillations

	// recall injection (see memory_recall.go)
	recall        bool
	recallTopN    int
	recallMinConf float64

	// procedural consolidation (see memory_consolidate.go)
	consolidate      bool
	consolidateEvery int
	minCluster       int

	mu sync.Mutex
	// per-session count of completed tasks already distilled, so a span is
	// distilled exactly once even though it stays in history on later requests.
	distilled map[string]int
	// de-dupe by content hash of the span, guarding against session-id churn.
	seenSpans map[string]struct{}
	// per-session frozen recall block, so injection is byte-stable across turns
	// (cache safety — see memory_recall.go).
	recallCache map[string]recallEntry
	// tailRecorded dedupes "recalled" events for the per-task tail overlay
	// (keyed "session\x00id"), which fires multiple times per session.
	tailRecorded map[string]struct{}
	// repoCache memoizes working-dir → (repo, commit) so we don't re-resolve
	// (and re-shell-out to git) on every request. index 0 = repo, 1 = commit.
	repoCache map[string][2]string
	// per-repo new-episode counters + in-flight guard for consolidation.
	sinceConsolidation map[string]int
	consolidating      map[string]bool
	consolidatedOnce   map[string]bool
}

// recallEntry is the frozen recall injection for one session.
type recallEntry struct {
	block string
	count int
	ids   []string
}

// MemoryFormerConfig configures ambient formation.
type MemoryFormerConfig struct {
	Store     MemoryStore
	Distiller Distiller
	Repo      string // fallback repo key when the request has no working directory
	Commit    string // fallback commit
	// RepoResolver maps a session's working directory (extracted from the request)
	// to its (repo, commit). Lets memory be scoped PER SESSION rather than to the
	// optimizer's launch directory — essential when one proxy serves sessions
	// across multiple repos. Nil → repo is the working-dir basename, no commit.
	RepoResolver func(cwd string) (repo, commit string)
	Logger       *slog.Logger
	// MaxConcurrent bounds in-flight distillations (default 2).
	MaxConcurrent int
	// Recall configures injecting relevant past episodes into the system prompt
	// (see memory_recall.go). Disabled when Recall.Enabled is false.
	Recall RecallConfig
	// Consolidate enables background procedural consolidation (see
	// memory_consolidate.go). ConsolidateEvery is how many new episodes in a repo
	// trigger a pass (default 6); MinCluster is the minimum episodes sharing
	// entities to form a procedure (default 3).
	Consolidate      bool
	ConsolidateEvery int
	MinCluster       int
}

// NewMemoryFormer builds a former. Returns nil if no store is configured.
func NewMemoryFormer(cfg MemoryFormerConfig) *MemoryFormer {
	if cfg.Store == nil {
		return nil
	}
	if cfg.Distiller == nil {
		cfg.Distiller = NewHTTPDistiller()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	max := cfg.MaxConcurrent
	if max <= 0 {
		max = 2
	}
	every := cfg.ConsolidateEvery
	if every <= 0 {
		every = 6
	}
	minCluster := cfg.MinCluster
	if minCluster <= 0 {
		minCluster = 3
	}
	return &MemoryFormer{
		store:              cfg.Store,
		distiller:          cfg.Distiller,
		repo:               cfg.Repo,
		commit:             cfg.Commit,
		repoResolver:       cfg.RepoResolver,
		logger:             cfg.Logger,
		recall:             cfg.Recall.Enabled,
		recallTopN:         cfg.Recall.TopN,
		recallMinConf:      cfg.Recall.MinConfidence,
		consolidate:        cfg.Consolidate,
		consolidateEvery:   every,
		minCluster:         minCluster,
		sem:                make(chan struct{}, max),
		distilled:          map[string]int{},
		seenSpans:          map[string]struct{}{},
		recallCache:        map[string]recallEntry{},
		tailRecorded:       map[string]struct{}{},
		repoCache:          map[string][2]string{},
		sinceConsolidation: map[string]int{},
		consolidating:      map[string]bool{},
		consolidatedOnce:   map[string]bool{},
	}
}

// Observe inspects a messages request body. If a task boundary was crossed
// (a new user instruction appeared beyond the previously-distilled ones), it
// kicks off async distillation of the completed span. headers/upstream/model
// are captured so the distiller can reuse the caller's credentials.
func (f *MemoryFormer) Observe(sessionID string, body []byte, headers http.Header, upstream string) {
	if f == nil {
		return
	}
	msgs, model := parseMessagesForFormation(body)
	instr := userInstructionIndices(msgs)
	// Completed tasks = all instructions except the latest (still in progress).
	completed := len(instr) - 1
	if completed < 1 {
		return
	}
	// Scope to the session's actual working directory, not the optimizer's
	// launch dir (one proxy can serve sessions across many repos).
	repo, commit := f.resolveRepo(body)

	f.mu.Lock()
	already := f.distilled[sessionID]
	if completed <= already {
		f.mu.Unlock()
		return
	}
	f.distilled[sessionID] = completed
	f.mu.Unlock()

	// Distill each newly-completed span (usually just one).
	for i := already; i < completed; i++ {
		span := msgs[instr[i]:instr[i+1]]
		if !spanHasActivity(span) {
			continue
		}
		transcript := renderSpan(span)
		if strings.TrimSpace(transcript) == "" {
			continue
		}
		h := sha256.Sum256([]byte(repo + "\x00" + transcript))
		key := hex.EncodeToString(h[:8])
		f.mu.Lock()
		_, dup := f.seenSpans[key]
		if !dup {
			f.seenSpans[key] = struct{}{}
		}
		f.mu.Unlock()
		if dup {
			continue
		}

		in := DistillInput{
			Transcript: transcript,
			Model:      model,
			Upstream:   upstream,
			Headers:    cloneAuthHeaders(headers),
			Repo:       repo,
			Commit:     commit,
			SessionID:  sessionID,
		}
		f.dispatch(in, key)
	}
}

// dispatch runs one distillation asynchronously under the concurrency bound.
func (f *MemoryFormer) dispatch(in DistillInput, spanKey string) {
	go func() {
		f.sem <- struct{}{}
		defer func() { <-f.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		defer cancel()
		f.logger.Info("memory: distilling completed task span", "session", in.SessionID, "model", in.Model)
		ep, err := f.distiller.Distill(ctx, in)
		if err != nil {
			// Visible at the default log level: a persistent failure here (e.g. an
			// auth-scope rejection) is exactly what silently prevents memory from
			// forming, so it must not hide at Debug.
			f.logger.Warn("memory: distill failed", "err", err, "session", in.SessionID)
			// allow a later request to retry this span
			f.mu.Lock()
			delete(f.seenSpans, spanKey)
			f.mu.Unlock()
			return
		}
		ep.ID = "ep_" + spanKey
		ep.Kind = KindEpisodic
		ep.Repo = in.Repo
		ep.About = "resource:repo:" + in.Repo
		ep.Entities = uniqueNonEmpty(ep.Files)
		ep.SessionID = in.SessionID
		ep.Client = in.Client
		ep.Model = in.Model
		ep.CodeState = CodeState{Commit: in.Commit}
		ep.SourceKind = "agent_inference"
		ep.Binding = "inferred"
		if ep.Confidence == 0 {
			ep.Confidence = 0.6
		}
		ep.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := f.store.WriteEpisode(*ep); err != nil {
			f.logger.Warn("memory: store failed", "err", err)
			return
		}
		f.logger.Info("memory: episode formed",
			"repo", ep.Repo, "task", ep.TaskSignature, "outcome", ep.Outcome)
		// Ambient confidence feedback: reinforce corroborated procedures and
		// credit/debit memories recalled into this session by E's outcome.
		f.applyFeedback(*ep)
		// A new episode may tip this repo over the consolidation threshold.
		f.noteEpisodeFormed(in)
	}()
}

// --- transcript inspection helpers ---

type formMsg struct {
	Role    string
	Content json.RawMessage
}

// parseMessagesForFormation extracts messages + model from a raw request body.
func parseMessagesForFormation(body []byte) ([]formMsg, string) {
	var req struct {
		Model    string    `json:"model"`
		Messages []formMsg `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil, ""
	}
	return req.Messages, req.Model
}

func (m *formMsg) UnmarshalJSON(b []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.Content = raw.Content
	return nil
}

// userInstructionIndices returns indices of user messages that carry a genuine
// text instruction (not tool_result-only turns). These delimit task spans.
func userInstructionIndices(msgs []formMsg) []int {
	var idxs []int
	for i, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if messageHasUserText(m.Content) {
			idxs = append(idxs, i)
		}
	}
	return idxs
}

// messageHasUserText reports whether a user message contains a text block /
// string content (an instruction) rather than only tool_result blocks.
func messageHasUserText(content json.RawMessage) bool {
	// string content
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// spanHasActivity reports whether a span contains tool use/results — i.e. it was
// a real unit of work worth remembering, not chit-chat.
func spanHasActivity(span []formMsg) bool {
	for _, m := range span {
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" || b.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

// renderSpan turns a task span into a compact text transcript for the distiller.
func renderSpan(span []formMsg) string {
	var b strings.Builder
	for _, m := range span {
		// string content
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			b.WriteString(m.Role)
			b.WriteString(": ")
			b.WriteString(truncate(s, 1500))
			b.WriteString("\n")
			continue
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, blk := range blocks {
			switch blk.Type {
			case "text":
				if strings.TrimSpace(blk.Text) != "" {
					b.WriteString(m.Role)
					b.WriteString(": ")
					b.WriteString(truncate(blk.Text, 1500))
					b.WriteString("\n")
				}
			case "tool_use":
				b.WriteString("[tool_use ")
				b.WriteString(blk.Name)
				b.WriteString(" ")
				b.WriteString(truncate(string(blk.Input), 300))
				b.WriteString("]\n")
			case "tool_result":
				tag := "[tool_result"
				if blk.IsError {
					tag = "[tool_result ERROR"
				}
				b.WriteString(tag)
				b.WriteString(": ")
				b.WriteString(truncate(toolResultText(blk.Content), 500))
				b.WriteString("]\n")
			}
		}
	}
	return b.String()
}

// toolResultText renders tool_result content (string or block array) to text.
func toolResultText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return string(content)
}

// cloneAuthHeaders copies only the headers the distiller needs.
func cloneAuthHeaders(h http.Header) http.Header {
	out := http.Header{}
	for _, k := range []string{"Authorization", "X-Api-Key", "Anthropic-Version", "Anthropic-Beta"} {
		if v := h.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// uniqueNonEmpty returns the distinct non-empty, trimmed values in order.
func uniqueNonEmpty(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
