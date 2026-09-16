package optimizer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Procedural consolidation. Episodes are formed one-per-task on the wire;
// procedures are the generalized "how to do X in this repo" knowledge distilled
// from a CLUSTER of related episodes. Consolidation runs in-session
// (background), triggered once a repo accumulates enough new episodes, reusing
// the live session's credentials for the LLM call — so it works under a
// subscription OAuth token with no extra setup.
//
// Locally, procedures are active immediately (it's the developer's own memory);
// the human gate in memory-capability-v2 applies at ORG promotion, not here.

// consolidateSystemPrompt asks the model to generalize a cluster into a
// procedure. The Claude Code identity is carried separately (OAuth scope).
const consolidateSystemPrompt = `You are consolidating a coding agent's episodic memory into one reusable PROCEDURE for a repository. You are given several past episodes (completed tasks) that touched overlapping files. Extract the generalizable know-how.

Return ONLY a JSON object, no prose:
- "title": short imperative name for the procedure (e.g. "add a new optimizer subcommand"). Lowercase, no punctuation.
- "description": 1-2 sentences on when this procedure applies and the general approach.
- "steps": array of short imperative steps that worked across these episodes (max 8).
- "pitfalls": array of short "watch out for X" warnings drawn from failures/errors seen (max 6, may be empty).
- "lesson": one durable sentence a future agent should remember.

Generalize — do not just restate one episode. Prefer what recurs across them. Be terse. Output ONLY the JSON object — no markdown code fences, no prose before or after.`

// maxConsolidationEpisodes caps how many episodes of a cluster are shown to the
// distiller (a large connected component would otherwise blow up the prompt).
// All episodes remain cited as evidence.
const maxConsolidationEpisodes = 15

// renderEpisodesForConsolidation renders a cluster compactly for the distiller.
// Shows the most recent maxConsolidationEpisodes.
func renderEpisodesForConsolidation(eps []Episode) string {
	if len(eps) > maxConsolidationEpisodes {
		sorted := append([]Episode(nil), eps...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].CreatedAt > sorted[j].CreatedAt })
		eps = sorted[:maxConsolidationEpisodes]
	}
	var b strings.Builder
	for i, e := range eps {
		fmt.Fprintf(&b, "Episode %d [%s]: %s\n", i+1, e.Outcome, e.TaskSignature)
		if e.Summary != "" {
			b.WriteString("  summary: " + truncate(e.Summary, 300) + "\n")
		}
		if e.Lesson != "" {
			b.WriteString("  lesson: " + truncate(e.Lesson, 200) + "\n")
		}
		if files := entsOf(e); len(files) > 0 {
			b.WriteString("  files: " + strings.Join(files, ", ") + "\n")
		}
		if len(e.Errors) > 0 {
			b.WriteString("  errors: " + strings.Join(e.Errors, "; ") + "\n")
		}
	}
	return b.String()
}

// parseProcedureJSON maps the distiller's procedure JSON into an Episode record
// with Kind=procedural (Summary=description, Actions=steps, Errors=pitfalls).
func parseProcedureJSON(text string) (*Episode, error) {
	s := extractJSONObject(text)
	var p struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Steps       []string `json:"steps"`
		Pitfalls    []string `json:"pitfalls"`
		Lesson      string   `json:"lesson"`
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, fmt.Errorf("parsing consolidated procedure: %w", err)
	}
	if p.Title == "" && p.Description == "" {
		return nil, fmt.Errorf("consolidated procedure is empty")
	}
	return &Episode{
		Kind:          KindProcedural,
		TaskSignature: p.Title,
		Summary:       p.Description,
		Actions:       p.Steps,
		Errors:        p.Pitfalls,
		Lesson:        p.Lesson,
	}, nil
}

// entsOf returns an episode's linking entities (files), preferring the explicit
// Entities field and falling back to Files.
func entsOf(e Episode) []string {
	if len(e.Entities) > 0 {
		return e.Entities
	}
	return e.Files
}

// clusterByEntities groups episodes into connected components where an edge is a
// shared entity (file). Only components with >= minCluster episodes are returned.
func clusterByEntities(eps []Episode, minCluster int) [][]Episode {
	n := len(eps)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	seen := map[string]int{} // entity -> representative episode index
	for i, e := range eps {
		for _, ent := range entsOf(e) {
			if j, ok := seen[ent]; ok {
				union(i, j)
			} else {
				seen[ent] = i
			}
		}
	}
	groups := map[int][]Episode{}
	for i := range eps {
		r := find(i)
		groups[r] = append(groups[r], eps[i])
	}
	var out [][]Episode
	for _, g := range groups {
		if len(g) >= minCluster {
			out = append(out, g)
		}
	}
	// stable order: by first episode id, so logs/results are deterministic
	sort.Slice(out, func(i, j int) bool { return out[i][0].ID < out[j][0].ID })
	return out
}

// unionEntities returns the sorted union of entities across a cluster.
func unionEntities(eps []Episode) []string {
	set := map[string]struct{}{}
	for _, e := range eps {
		for _, ent := range entsOf(e) {
			set[ent] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// procedureID is a stable id for the cluster's episode-id set, so re-running
// consolidation on the same cluster does not create duplicates.
func procedureID(repo string, episodeIDs []string) string {
	ids := append([]string(nil), episodeIDs...)
	sort.Strings(ids)
	h := sha256.Sum256([]byte(repo + "\x00" + strings.Join(ids, ",")))
	return "proc_" + hex.EncodeToString(h[:8])
}

// noteEpisodeFormed increments the per-repo new-episode counter and kicks off a
// background consolidation once the threshold is crossed (one at a time per
// repo). Called from dispatch after an episode is written.
func (f *MemoryFormer) noteEpisodeFormed(in DistillInput) {
	if !f.consolidate {
		return
	}
	f.mu.Lock()
	f.sinceConsolidation[in.Repo]++
	// Trigger on the FIRST episode of the process (clears any backlog and is
	// robust to restarts, since the counter is per-process), then every N after.
	first := !f.consolidatedOnce[in.Repo]
	trigger := (first || f.sinceConsolidation[in.Repo] >= f.consolidateEvery) && !f.consolidating[in.Repo]
	if trigger {
		f.consolidating[in.Repo] = true
		f.consolidatedOnce[in.Repo] = true
		f.sinceConsolidation[in.Repo] = 0
	}
	f.mu.Unlock()
	if trigger {
		go f.consolidateRepo(in)
	}
}

// consolidateRepo clusters the repo's episodes and distills a procedure for each
// cluster carrying enough NOT-yet-consolidated episodes. Best-effort.
func (f *MemoryFormer) consolidateRepo(in DistillInput) {
	defer func() {
		f.mu.Lock()
		f.consolidating[in.Repo] = false
		f.mu.Unlock()
	}()

	eps, err := f.store.ListEpisodes(in.Repo, 0)
	if err != nil {
		f.logger.Warn("memory: consolidation list failed", "err", err, "repo", in.Repo)
		return
	}
	var episodic []Episode
	covered := map[string]struct{}{} // episodes already cited by a procedure
	existingProc := map[string]struct{}{}
	for _, e := range eps {
		if e.Kind == KindProcedural {
			existingProc[e.ID] = struct{}{}
			for _, id := range e.EpisodeIDs {
				covered[id] = struct{}{}
			}
			continue
		}
		episodic = append(episodic, e)
	}

	clusters := clusterByEntities(episodic, f.minCluster)
	for _, cl := range clusters {
		newCount := 0
		for _, e := range cl {
			if _, ok := covered[e.ID]; !ok {
				newCount++
			}
		}
		if newCount < f.minCluster {
			continue // not enough fresh signal to (re)consolidate this cluster
		}
		ids := make([]string, len(cl))
		for i, e := range cl {
			ids[i] = e.ID
		}
		id := procedureID(in.Repo, ids)
		if _, exists := existingProc[id]; exists {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		proc, derr := f.distiller.DistillProcedure(ctx, ProcedureInput{
			Episodes: cl, Model: in.Model, Upstream: in.Upstream, Headers: in.Headers, Repo: in.Repo,
		})
		cancel()
		if derr != nil {
			f.logger.Warn("memory: consolidation failed", "err", derr, "repo", in.Repo)
			continue
		}
		proc.ID = id
		proc.Kind = KindProcedural
		proc.Repo = in.Repo
		proc.About = "resource:repo:" + in.Repo
		proc.EpisodeIDs = ids
		proc.Entities = unionEntities(cl)
		proc.SourceKind = "consolidation"
		proc.Binding = "inferred"
		if proc.Confidence == 0 {
			proc.Confidence = 0.7
		}
		proc.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := f.store.WriteEpisode(*proc); err != nil {
			f.logger.Warn("memory: writing procedure failed", "err", err, "repo", in.Repo)
			continue
		}
		f.logger.Info("memory: procedure consolidated",
			"repo", in.Repo, "task", proc.TaskSignature, "from_episodes", len(ids))
	}
}
