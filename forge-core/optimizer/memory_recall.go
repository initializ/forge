package optimizer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Recall injection: before a request is forwarded, the optimizer looks up the
// episodes it has learned for this repo and injects a compact "relevant past
// work" block into the system prompt, so the model benefits from prior sessions
// without any client wiring (§12.8 push retrieval via body rewrite).
//
// Cache safety is the governing constraint. Claude Code sets cache_control
// breakpoints across system/tools/messages, and caching matches on the prefix
// of content leading to each breakpoint. So the injected block MUST be
// byte-identical on every turn of a session — otherwise each turn shifts the
// prefix and busts every downstream breakpoint. Two rules guarantee that:
//
//  1. Freeze per session — recall is computed once on the first request of a
//     session and reused verbatim for the session's lifetime.
//  2. Exclude the current session's own episodes — past episodes are immutable,
//     so a session-frozen block computed from them is naturally stable, and we
//     avoid a feedback loop where a session reads back what it just wrote.
//
// The block is appended to the END of the system content with NO cache_control
// of its own (Claude Code may already use all four breakpoints); it becomes part
// of the cached prefix from the first turn it appears and is a cache hit
// thereafter.

// defaultRecallTopN caps how many episodes are injected.
const defaultRecallTopN = 5

// recallBlockPrefix marks the injected block so the model (and a human reading
// the request) knows it is auto-formed memory, not user instruction.
const recallBlockPrefix = "[forge memory] Relevant past work in this repo (learned automatically from prior sessions; may be stale — verify against the current code):"

// RecallConfig turns on recall injection within the MemoryFormer.
type RecallConfig struct {
	Enabled bool
	TopN    int
	// MinConfidence gates injection: only memories whose effective confidence is
	// at least this are injected (default 0.5). Below it, a memory still exists
	// and can recover via feedback, but is not injected.
	MinConfidence float64
}

// Inject returns body with a session-frozen recall block appended to the system
// prompt, plus the number of episodes injected. On any parse issue, or when
// recall is disabled or empty, it returns the body unchanged and 0.
func (f *MemoryFormer) Inject(sessionID string, body []byte) ([]byte, int) {
	if f == nil || !f.recall {
		return body, 0
	}

	out := body
	total := 0

	// Scope recall to the SESSION's repo (from its working directory), not the
	// optimizer's launch dir — so a session in repo B never gets repo A's memory.
	repo, _ := f.resolveRepo(body)

	// 1. Frozen, task-AGNOSTIC block (procedures + top general lessons) appended
	//    to the SYSTEM prompt. Computed once per session and reused verbatim, so
	//    it never disturbs the cached prefix — and a procedure consolidated later
	//    in the session is NOT retro-injected here; it surfaces in a new session.
	block, n, frozenIDs := f.frozenBlock(sessionID, repo)
	if block != "" {
		if b, err := appendSystemBlock(out, block); err == nil {
			out = b
			total += n
		}
	}

	// 2. Task-SPECIFIC overlay appended to the latest user turn (the uncached
	//    tail), re-targeted to the current instruction. This is where within-
	//    session task relevance lives, mirroring how Claude Code keeps task
	//    content in the message stream rather than the cached prefix.
	tail, tn := f.tailOverlay(sessionID, body, repo, frozenIDs)
	if tail != "" {
		if b, err := appendToLastUserMessage(out, tail); err == nil {
			out = b
			total += tn
		}
	}

	return out, total
}

// frozenBlock returns the session-frozen, task-agnostic recall block (block,
// count, ids), computing it once on first use.
func (f *MemoryFormer) frozenBlock(sessionID, repo string) (string, int, []string) {
	f.mu.Lock()
	if cached, ok := f.recallCache[sessionID]; ok {
		f.mu.Unlock()
		return cached.block, cached.count, cached.ids
	}
	f.mu.Unlock()

	block, ids := f.computeRecall(sessionID, repo)

	f.mu.Lock()
	f.recallCache[sessionID] = recallEntry{block: block, count: len(ids), ids: ids}
	f.mu.Unlock()
	return block, len(ids), ids
}

// computeRecall builds the frozen block: procedures-dominant and task-agnostic
// (ranked by procedural boost + confidence + recency, NOT the current task), so
// it applies to any task in the session and needs no re-targeting.
func (f *MemoryFormer) computeRecall(sessionID, repo string) (string, []string) {
	eps, err := f.store.ListEpisodes(repo, 0)
	if err != nil || len(eps) == 0 {
		return "", nil
	}

	// Effective confidence per memory = base prior + accumulated feedback,
	// minus a staleness penalty when the repo has moved past the commit the
	// memory was formed at. Confidence-gates and ranks the candidates.
	fb, _ := f.store.FeedbackAgg()
	// minConf is the injection gate: 0 disables it (inject regardless of
	// confidence); a negative value is clamped to 0. Callers that want the
	// standard gate set it explicitly (the CLI defaults it to 0.5).
	minConf := f.recallMinConf
	if minConf < 0 {
		minConf = 0
	}

	candidates := eps[:0:0]
	confOf := map[string]float64{}
	for _, e := range eps {
		if e.SessionID == sessionID {
			continue // don't recall our own in-flight session (see file header)
		}
		if strings.TrimSpace(e.TaskSignature) == "" && strings.TrimSpace(e.Lesson) == "" {
			continue
		}
		conf := EffectiveConfidence(e.Confidence, fb[e.ID])
		// Staleness: down-weight memory formed against a now-moved commit.
		if f.commit != "" && e.CodeState.Commit != "" && e.CodeState.Commit != f.commit {
			conf *= 0.85
		}
		if conf < minConf {
			continue // below the bar — not injected (can still recover via feedback)
		}
		confOf[e.ID] = conf
		candidates = append(candidates, e)
	}
	if len(candidates) == 0 {
		return "", nil
	}

	// Task-agnostic rank: procedures first (recallScore's procedural boost),
	// then effective confidence, then recency. NO current-task signal — task
	// relevance is the tail overlay's job. Deterministic → byte-stable frozen
	// block for the whole session (cache-safe).
	scoreOf := func(e Episode) float64 {
		return float64(recallScore(e)) + 2*confOf[e.ID]
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		si, sj := scoreOf(candidates[i]), scoreOf(candidates[j])
		if si != sj {
			return si > sj
		}
		return candidates[i].CreatedAt > candidates[j].CreatedAt
	})

	top := f.recallTopN
	if top <= 0 {
		top = defaultRecallTopN
	}
	if len(candidates) > top {
		candidates = candidates[:top]
	}

	// Record + log which episodes were injected (once per session — computeRecall
	// runs once per session id since the result is frozen/cached). This is what
	// makes "was this memory ever used?" answerable.
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ID
	}
	if len(ids) > 0 {
		f.logger.Info("memory: frozen recall block (task-agnostic)", "session", sessionID, "count", len(ids), "ids", strings.Join(ids, ","))
		go func() {
			if err := f.store.RecordRecall(sessionID, ids); err != nil {
				f.logger.Debug("memory: recording recall failed", "err", err)
			}
		}()
	}
	return renderRecallBlock(candidates), ids
}

// tailOverlayTopN caps how many task-specific episodes ride in the tail overlay.
const tailOverlayTopN = 3

// tailOverlay builds a compact block of episodes relevant to the CURRENT task
// (the latest user instruction), for injection into the message tail. Returns
// ("",0) unless the latest message is a fresh user text instruction — on
// tool_result continuations we leave the turn alone. Excludes episodes already
// in the frozen block, gates by confidence, and requires actual relevance.
func (f *MemoryFormer) tailOverlay(sessionID string, body []byte, repo string, frozenIDs []string) (string, int) {
	msgs, _ := parseMessagesForFormation(body)
	if len(msgs) == 0 {
		return "", 0
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !messageHasUserText(last.Content) {
		return "", 0 // mid-tool-loop or non-user tail — don't disturb it
	}
	q := extractTaskQuery(body)
	if q.empty() {
		return "", 0
	}

	frozen := map[string]struct{}{}
	for _, id := range frozenIDs {
		frozen[id] = struct{}{}
	}
	minConf := f.recallMinConf
	if minConf < 0 {
		minConf = 0
	}
	fb, _ := f.store.FeedbackAgg()
	eps, err := f.store.ListEpisodes(repo, 0)
	if err != nil {
		return "", 0
	}

	type scored struct {
		e    Episode
		rel  float64
		conf float64
	}
	var cands []scored
	for _, e := range eps {
		if e.Kind == KindProcedural { // procedures live in the frozen block
			continue
		}
		if e.SessionID == sessionID {
			continue
		}
		if _, dup := frozen[e.ID]; dup {
			continue
		}
		rel := relevanceScore(e, q)
		if rel <= 0 {
			continue // only inject episodes actually relevant to THIS task
		}
		conf := EffectiveConfidence(e.Confidence, fb[e.ID])
		if conf < minConf {
			continue
		}
		cands = append(cands, scored{e, rel, conf})
	}
	if len(cands) == 0 {
		return "", 0
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].rel != cands[j].rel {
			return cands[i].rel > cands[j].rel
		}
		if cands[i].conf != cands[j].conf {
			return cands[i].conf > cands[j].conf
		}
		return cands[i].e.CreatedAt > cands[j].e.CreatedAt
	})
	if len(cands) > tailOverlayTopN {
		cands = cands[:tailOverlayTopN]
	}

	picked := make([]Episode, len(cands))
	ids := make([]string, len(cands))
	for i, c := range cands {
		picked[i] = c.e
		ids[i] = c.e.ID
	}
	f.recordTailRecall(sessionID, ids)
	return renderTailBlock(picked), len(picked)
}

// recordTailRecall records recall events for tail-injected episodes, deduped
// per (session,id) so the per-task overlay doesn't spam the recall log.
func (f *MemoryFormer) recordTailRecall(sessionID string, ids []string) {
	var fresh []string
	f.mu.Lock()
	for _, id := range ids {
		key := sessionID + "\x00" + id
		if _, ok := f.tailRecorded[key]; ok {
			continue
		}
		f.tailRecorded[key] = struct{}{}
		fresh = append(fresh, id)
	}
	f.mu.Unlock()
	if len(fresh) > 0 {
		f.logger.Info("memory: tail overlay (task-relevant)", "session", sessionID, "ids", strings.Join(fresh, ","))
		go func() { _ = f.store.RecordRecall(sessionID, fresh) }()
	}
}

// renderTailBlock formats task-relevant episodes for the tail overlay.
func renderTailBlock(eps []Episode) string {
	var b strings.Builder
	b.WriteString("[forge memory] Relevant past work for this task (from earlier sessions; verify against current code):\n")
	for _, e := range eps {
		line := "- " + strings.TrimSpace(e.TaskSignature)
		if lesson := strings.TrimSpace(e.Lesson); lesson != "" {
			line += " — " + lesson
		} else if sum := strings.TrimSpace(e.Summary); sum != "" {
			line += " — " + truncate(sum, 160)
		}
		if len(e.Files) > 0 {
			line += " (" + strings.Join(e.Files, ", ") + ")"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// appendToLastUserMessage appends a text block to the last message's content
// (which the caller has verified is a user text turn), preserving all other
// message/request fields. The block lands in the uncached tail, so it does not
// disturb the cached prefix.
func appendToLastUserMessage(body []byte, text string) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	raw, ok := top["messages"]
	if !ok {
		return nil, fmt.Errorf("no messages")
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil || len(msgs) == 0 {
		return nil, fmt.Errorf("no messages")
	}
	var last map[string]json.RawMessage
	if err := json.Unmarshal(msgs[len(msgs)-1], &last); err != nil {
		return nil, err
	}
	var role string
	_ = json.Unmarshal(last["role"], &role)
	if role != "user" {
		return nil, fmt.Errorf("last message is not a user turn")
	}

	newBlock, _ := json.Marshal(map[string]any{"type": "text", "text": text})
	var blocks []json.RawMessage
	if c, ok := last["content"]; ok && len(c) > 0 {
		var s string
		if json.Unmarshal(c, &s) == nil {
			sb, _ := json.Marshal(map[string]any{"type": "text", "text": s})
			blocks = append(blocks, sb)
		} else if err := json.Unmarshal(c, &blocks); err != nil {
			return nil, fmt.Errorf("last message content not string or blocks: %w", err)
		}
	}
	blocks = append(blocks, newBlock)
	nb, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	last["content"] = nb
	lm, err := json.Marshal(last)
	if err != nil {
		return nil, err
	}
	msgs[len(msgs)-1] = lm
	mm, err := json.Marshal(msgs)
	if err != nil {
		return nil, err
	}
	top["messages"] = mm
	return json.Marshal(top)
}

// taskQuery is the relevance signal extracted from the session's opening
// request: significant words from the latest user instruction, plus file paths
// referenced in the request (tool inputs / instruction).
type taskQuery struct {
	tokens map[string]struct{}
	files  map[string]struct{}
}

func (q taskQuery) empty() bool { return len(q.tokens) == 0 && len(q.files) == 0 }

// extractTaskQuery pulls the current-task signal from a request body: the latest
// user text instruction (tokenized) and any file-path-like tokens across the
// recent messages.
func extractTaskQuery(body []byte) taskQuery {
	q := taskQuery{tokens: map[string]struct{}{}, files: map[string]struct{}{}}
	msgs, _ := parseMessagesForFormation(body)
	if len(msgs) == 0 {
		return q
	}
	// Latest user text instruction drives the token relevance.
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && messageHasUserText(msgs[i].Content) {
			for tok := range tokenize(userText(msgs[i].Content)) {
				q.tokens[tok] = struct{}{}
			}
			break
		}
	}
	// File paths anywhere in the (recent) request — from tool_use inputs, text,
	// etc. Rendering the tail keeps this bounded.
	tail := msgs
	if len(tail) > 12 {
		tail = tail[len(tail)-12:]
	}
	for f := range pathTokens(renderSpan(tail)) {
		q.files[f] = struct{}{}
	}
	return q
}

// userText returns the text of a user message (string or text blocks).
func userText(content json.RawMessage) string {
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
				b.WriteString(" ")
			}
		}
		return b.String()
	}
	return ""
}

// tokenize lowercases and keeps significant words (len >= 4) as a set.
func tokenize(s string) map[string]struct{} {
	out := map[string]struct{}{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 4 {
			out[strings.ToLower(cur.String())] = struct{}{}
		}
		cur.Reset()
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// pathTokens extracts file-path-like tokens (containing '/' and a '.') from text.
func pathTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '"' || r == ',' || r == '[' || r == ']' || r == '{' || r == '}' || r == ':' || r == '(' || r == ')'
	}) {
		field = strings.Trim(field, ".,'\"`")
		if strings.Contains(field, "/") && strings.Contains(field, ".") && len(field) <= 200 {
			out[field] = struct{}{}
		}
	}
	return out
}

// relevanceScore rates a memory against the opening-task query: file overlap is
// the strongest signal (a memory that touched a file this task touches), then
// word overlap with the task signature / lesson.
func relevanceScore(e Episode, q taskQuery) float64 {
	if q.empty() {
		return 0
	}
	score := 0.0
	for _, ent := range entsOf(e) {
		if _, ok := q.files[ent]; ok {
			score += 3 // exact file match — strong
			continue
		}
		// path suffix / substring match (e.g. basename referenced)
		for qf := range q.files {
			if strings.HasSuffix(qf, ent) || strings.HasSuffix(ent, qf) {
				score += 2
				break
			}
		}
	}
	if len(q.tokens) > 0 {
		for tok := range tokenize(e.TaskSignature + " " + e.Lesson) {
			if _, ok := q.tokens[tok]; ok {
				score++
			}
		}
	}
	return score
}

// recallScore prioritizes procedures (generalized know-how) over raw episodes,
// then records that carry a reusable lesson and succeeded.
func recallScore(e Episode) int {
	s := 0
	if e.Kind == KindProcedural {
		s += 10 // procedures are denser, higher-signal — inject them first
	}
	if strings.TrimSpace(e.Lesson) != "" {
		s += 2
	}
	if e.Outcome == OutcomeSuccess {
		s++
	}
	return s
}

// renderRecallBlock formats episodes as a compact bulleted block.
func renderRecallBlock(eps []Episode) string {
	var b strings.Builder
	b.WriteString(recallBlockPrefix)
	b.WriteString("\n")
	for _, e := range eps {
		mark := map[string]string{
			OutcomeSuccess:   "✓",
			OutcomeFailure:   "✗",
			OutcomeAbandoned: "∅",
		}[e.Outcome]
		if mark == "" {
			mark = "·"
		}
		line := "- " + mark + " " + strings.TrimSpace(e.TaskSignature)
		if lesson := strings.TrimSpace(e.Lesson); lesson != "" {
			line += " — " + lesson
		} else if sum := strings.TrimSpace(e.Summary); sum != "" {
			line += " — " + truncate(sum, 160)
		}
		if len(e.Files) > 0 {
			line += " (" + strings.Join(e.Files, ", ") + ")"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// appendSystemBlock adds a text block to the request's system prompt, preserving
// every other field byte-for-byte (only the "system" key is rewritten). Handles
// system as a string, a block array, or absent.
func appendSystemBlock(body []byte, text string) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}

	newBlock := map[string]any{"type": "text", "text": text}

	var blocks []json.RawMessage
	if raw, ok := top["system"]; ok && len(raw) > 0 {
		// string form → wrap as a text block
		var s string
		if json.Unmarshal(raw, &s) == nil {
			sb, _ := json.Marshal(map[string]any{"type": "text", "text": s})
			blocks = append(blocks, sb)
		} else {
			// array form
			if err := json.Unmarshal(raw, &blocks); err != nil {
				return nil, fmt.Errorf("system is neither string nor block array: %w", err)
			}
		}
	}
	nb, err := json.Marshal(newBlock)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, nb)

	merged, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	top["system"] = merged
	return json.Marshal(top)
}
