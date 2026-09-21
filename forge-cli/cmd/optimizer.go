package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/initializ/forge/forge-core/optimizer"
)

var (
	optimizerListen            string
	optimizerUpstream          string
	optimizerTelemetry         string
	optimizerUsageLog          string
	optimizerCompress          bool
	optimizerCompressStore     string
	optimizerCompressMinT      int
	optimizerCompressProtect   int
	optimizerInbandExpand      bool
	optimizerMemory            bool
	optimizerMemoryRecall      bool
	optimizerMemoryMinConf     float64
	optimizerMemoryConsolidate bool
	optimizerMemoryStore       string
	optimizerClaudeBin         string
	optimizerStatsSession      string
	optimizerStatsJSON         bool
	optimizerMemoryRepo        string
	optimizerMemoryJSON        bool
	optimizerPrintSetup        bool
	optimizerQuietBanner       bool
)

// forgeHome is the shared ~/.forge directory (fallback ./.forge when the home
// dir can't be determined). Putting the usage log, process log, compression
// store, and pricing overrides here — rather than a cwd-relative ./.forge —
// means every session, launched from any directory, aggregates into ONE place
// that the dashboard and `savings` command read.
func forgeHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".forge")
	}
	return ".forge"
}

// forgeFile resolves a file name under the shared ~/.forge directory.
func forgeFile(name string) string { return filepath.Join(forgeHome(), name) }

// defaultUsageLog is the NDJSON usage record written when --usage-log is left at
// its default — shared across all sessions under ~/.forge.
var defaultUsageLog = forgeFile("optimizer-usage.jsonl")

// The ctxzip compression store is a self-healing retrieval cache with no
// cross-session value (offloaded originals are content-addressed and TTL'd, and
// the single-writer process is the natural owner). So it is EPHEMERAL by
// default: when --compress-store is left empty the optimizer creates a
// per-process temp bbolt file and removes it on shutdown. Pass --compress-store
// <path> to keep a durable/shared store instead.

// defaultMemoryStore is the local episodic memory store (NDJSON) under the
// shared ~/.forge directory — the OSS memory provider. When a control-plane URL
// is configured a remote provider becomes authoritative and this becomes a
// write-through cache; the record shape is identical either way.
var defaultMemoryStore = forgeFile("optimizer-memory.jsonl")

// optimizerCmd starts the local context optimizer that Claude Code routes
// through via ANTHROPIC_BASE_URL: it meters real token usage and, with
// --compress, compresses bulky conversation content and serves context_expand.
var optimizerCmd = &cobra.Command{
	Use:   "optimizer",
	Short: "Run the local context optimizer for Claude Code (and other Anthropic clients)",
	Long: `Starts a local server that Claude Code routes through by setting
ANTHROPIC_BASE_URL. It forwards requests to the Anthropic API (or an org's
existing gateway), meters real token usage per request, and — with --compress —
compresses bulky content in the conversation before it reaches the model,
serving a context_expand tool so the model can retrieve anything it dropped.

Point Claude Code at it:

  export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
  claude mcp add --transport http forge-optimizer http://127.0.0.1:8787/mcp   # with --compress
  claude

Gateway chaining: if ANTHROPIC_BASE_URL already points at your org gateway when
this starts, the optimizer captures it as its upstream and forwards to it, so
you can insert the optimizer in front without losing your gateway.`,
	RunE: runOptimizer,
}

// optimizerClaudeCmd wraps Claude Code: it starts the optimizer in-process and
// launches the real `claude` binary as a child with ANTHROPIC_BASE_URL pointed
// at it — like `headroom wrap claude`. The proxy lives exactly as long as the
// Claude Code session.
var optimizerClaudeCmd = &cobra.Command{
	Use:   "claude [-- claude args...]",
	Short: "Launch Claude Code routed through the optimizer (starts the proxy, then runs claude)",
	Long: `Starts the optimizer in-process and launches the real ` + "`claude`" + ` binary with
ANTHROPIC_BASE_URL set to it, so Claude Code is transparently routed through
context compression + metering. When claude exits, the optimizer shuts down.

Compression is ON by default for this subcommand (pass --compress=false to
disable). Pass flags to claude after --:

  forge optimizer claude
  forge optimizer claude --compress-protect-recent 2 -- --model opus`,
	RunE:                  runOptimizerClaude,
	DisableFlagsInUseLine: true,
}

func init() {
	// Shared flags live on PersistentFlags so both `optimizer` and
	// `optimizer claude` accept them.
	pf := optimizerCmd.PersistentFlags()
	pf.StringVar(&optimizerListen, "listen", optimizer.DefaultListen, "address to listen on (host:port)")
	pf.StringVar(&optimizerUpstream, "upstream", "", "upstream base URL to forward to, e.g. an org gateway (precedence: managed settings > this flag > $FORGE_OPTIMIZER_UPSTREAM > ~/.forge/settings.json > $ANTHROPIC_BASE_URL > "+optimizer.DefaultUpstream+")")
	pf.StringVar(&optimizerTelemetry, "telemetry-url", "", "control-plane URL to POST token usage to (default: $FORGE_OPTIMIZER_TELEMETRY_URL)")
	pf.StringVar(&optimizerUsageLog, "usage-log", defaultUsageLog, "local NDJSON file to append per-request usage to (empty to disable)")
	pf.BoolVar(&optimizerCompress, "compress", false, "compress bulky conversation content before it reaches the model (EXPERIMENTAL; default ON for `optimizer claude`)")
	pf.StringVar(&optimizerCompressStore, "compress-store", "", "bbolt store for compressed originals (default: ephemeral per-process temp file, removed on exit; pass a path to persist/share)")
	pf.IntVar(&optimizerCompressMinT, "compress-min-tokens", 50, "minimum tokens a block must have to be compressed")
	pf.IntVar(&optimizerCompressProtect, "compress-protect-recent", 0, "leave the last N messages uncompressed so fresh tool output stays verbatim (0 = compress all history)")
	pf.BoolVar(&optimizerInbandExpand, "inband-expand", false, "resolve context_expand by buffering marker-bearing turns server-side (OPT-IN: disables true streaming on those turns). Default OFF — retrieval is served over MCP and auto-registered with Claude Code by `optimizer claude`")
	pf.BoolVar(&optimizerMemory, "memory", false, "form episodic memory by distilling completed task spans off the wire (default ON for `optimizer claude`)")
	pf.BoolVar(&optimizerMemoryRecall, "memory-recall", true, "inject relevant past episodes/procedures for this repo into the system prompt (frozen per session for cache safety)")
	pf.Float64Var(&optimizerMemoryMinConf, "memory-recall-min-confidence", 0.5, "only inject memories whose effective confidence is >= this (0-1; higher is stricter, 0 disables the gate). Below it a memory still exists and can recover via feedback")
	pf.BoolVar(&optimizerMemoryConsolidate, "memory-consolidate", true, "background: generalize clusters of related episodes into procedural memory (reuses the live session's credentials)")
	pf.StringVar(&optimizerMemoryStore, "memory-store", defaultMemoryStore, "local NDJSON episodic memory store")
	pf.BoolVar(&optimizerQuietBanner, "quiet", false, "suppress the startup setup banner")

	optimizerCmd.Flags().BoolVar(&optimizerPrintSetup, "print-setup", false, "print the Claude Code setup snippet and exit")
	optimizerClaudeCmd.Flags().StringVar(&optimizerClaudeBin, "claude-bin", "", "path to the claude binary (default: 'claude' on PATH)")
	optimizerStatsCmd.Flags().StringVar(&optimizerStatsSession, "session", "", "show only this session id")
	optimizerStatsCmd.Flags().BoolVar(&optimizerStatsJSON, "json", false, "print raw JSON")
	optimizerMemoryCmd.Flags().StringVar(&optimizerMemoryStore, "memory-store", defaultMemoryStore, "local NDJSON episodic memory store")
	optimizerMemoryCmd.Flags().StringVar(&optimizerMemoryRepo, "repo", "", "show only episodes for this repo")
	optimizerMemoryCmd.Flags().BoolVar(&optimizerMemoryJSON, "json", false, "print raw JSON")

	optimizerCmd.AddCommand(optimizerClaudeCmd)
	optimizerCmd.AddCommand(optimizerStatsCmd)
	optimizerMemoryCmd.AddCommand(optimizerMemoryFeedbackCmd)
	optimizerCmd.AddCommand(optimizerMemoryCmd)
	rootCmd.AddCommand(optimizerCmd)
}

// optimizerStatsCmd queries a running optimizer's /stats and prints usage
// overall and per session.
var optimizerStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show optimizer usage (overall and per Claude Code session)",
	Long: `Fetches /stats from a running optimizer (on --listen, default ` + optimizer.DefaultListen + `)
and prints token usage totals plus a per-session breakdown. Sessions are keyed
by a stable hash of the model + system prompt, so a resumed conversation keeps
its own line.`,
	RunE: runOptimizerStats,
}

func runOptimizerStats(_ *cobra.Command, _ []string) error {
	listen := resolveListen()
	url := "http://" + listen + "/stats"
	if optimizerStatsSession != "" {
		url += "?session=" + optimizerStatsSession
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("no optimizer reachable at %s (start one with `forge optimizer`): %w", listen, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("optimizer /stats returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if optimizerStatsJSON {
		fmt.Println(string(body))
		return nil
	}
	if optimizerStatsSession != "" {
		return renderOneSession(body)
	}
	return renderStats(body)
}

func renderStats(body []byte) error {
	var snap optimizer.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return err
	}
	fmt.Printf("forge optimizer — usage (uptime %s)\n\n", (time.Duration(snap.UptimeSeconds) * time.Second).String())
	printTotalsLine("OVERALL", snap.Totals)

	if len(snap.Sessions) == 0 {
		fmt.Println("\nNo sessions recorded yet.")
		return nil
	}

	// Sort sessions by last-seen, most recent first.
	ids := make([]string, 0, len(snap.Sessions))
	for id := range snap.Sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return snap.Sessions[ids[i]].LastSeen > snap.Sessions[ids[j]].LastSeen
	})

	fmt.Printf("\nPer session (%d):\n", len(ids))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SESSION\tREQS\tINPUT\tOUTPUT\tCACHE_RD\tSAVED\tEXPAND\tLAST SEEN")
	for _, id := range ids {
		s := snap.Sessions[id]
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			id, s.Requests, s.InputTokens, s.OutputTokens,
			s.CacheReadInputTokens, s.CompressionSavedTokens, s.Expansions, s.LastSeen)
	}
	return tw.Flush()
}

func renderOneSession(body []byte) error {
	var out struct {
		Session string                 `json:"session"`
		Stats   optimizer.SessionStats `json:"stats"`
		Recent  []optimizer.Report     `json:"recent"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	fmt.Printf("Session %s\n", out.Session)
	fmt.Printf("  first seen: %s   last seen: %s\n\n", out.Stats.FirstSeen, out.Stats.LastSeen)
	printTotalsLine("TOTALS", out.Stats.Totals)
	fmt.Printf("\nRecent requests: %d\n", len(out.Recent))
	return nil
}

func printTotalsLine(label string, t optimizer.Totals) {
	fmt.Printf("%s: %d req · in %d · out %d · cache_read %d · cache_create %d",
		label, t.Requests, t.InputTokens, t.OutputTokens, t.CacheReadInputTokens, t.CacheCreationInputTokens)
	if t.CompressionSavedTokens > 0 || t.CompressedBlocks > 0 {
		fmt.Printf(" · saved %d (%d blocks)", t.CompressionSavedTokens, t.CompressedBlocks)
	}
	if t.Expansions > 0 {
		fmt.Printf(" · expand %d", t.Expansions)
	}
	if t.Errors > 0 {
		fmt.Printf(" · errors %d", t.Errors)
	}
	fmt.Println()
}

// optimizerMemoryCmd lists the local episodic memory the optimizer has formed.
var optimizerMemoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Show episodic memory formed from Claude Code sessions (local, per repo)",
	Long: `Reads the local episodic memory store the optimizer writes when run with
--memory (default ON for ` + "`forge optimizer claude`" + `) and lists the distilled
episodes, most recent first. Memory is stored locally under ~/.forge; when a
control-plane URL is configured it also flows there for cross-session and org
learning.`,
	RunE: runOptimizerMemory,
}

func runOptimizerMemory(_ *cobra.Command, _ []string) error {
	store, err := optimizer.NewFileMemoryStore(firstNonEmpty(optimizerMemoryStore, defaultMemoryStore))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	eps, err := store.ListEpisodes(optimizerMemoryRepo, 0)
	if err != nil {
		return err
	}
	if optimizerMemoryJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(eps)
	}
	if len(eps) == 0 {
		fmt.Printf("No episodes yet in %s — run `forge optimizer claude` (memory is ON by default).\n",
			firstNonEmpty(optimizerMemoryStore, defaultMemoryStore))
		return nil
	}
	fb, _ := store.FeedbackAgg()
	nProc := 0
	for _, e := range eps {
		if e.Kind == optimizer.KindProcedural {
			nProc++
		}
	}
	fmt.Printf("forge optimizer — memory (%d episodes, %d procedures)\n\n", len(eps)-nProc, nProc)
	for _, e := range eps {
		mark := map[string]string{
			optimizer.OutcomeSuccess:   "✓",
			optimizer.OutcomeFailure:   "✗",
			optimizer.OutcomeAbandoned: "∅",
		}[e.Outcome]
		if e.Kind == optimizer.KindProcedural {
			mark = "⚙"
		} else if mark == "" {
			mark = "·"
		}
		label := e.Repo
		if e.Kind == optimizer.KindProcedural {
			label = fmt.Sprintf("%s · procedure from %d episodes", e.Repo, len(e.EpisodeIDs))
		}
		fmt.Printf("%s  %-40s  [%s]\n", mark, e.TaskSignature, label)
		if e.Summary != "" {
			fmt.Printf("     %s\n", e.Summary)
		}
		if e.Lesson != "" {
			fmt.Printf("     lesson: %s\n", e.Lesson)
		}
		if len(e.Files) > 0 {
			fmt.Printf("     files: %s\n", strings.Join(e.Files, ", "))
		}
		conf := optimizer.EffectiveConfidence(e.Confidence, fb[e.ID])
		gate := ""
		if conf < 0.5 {
			gate = " (below inject threshold)"
		}
		fmt.Printf("     conf %.2f%s · %s · %s\n\n", conf, gate, e.CreatedAt, e.CodeState.Commit)
	}
	return nil
}

// optimizerMemoryFeedbackCmd records explicit 👍/👎 to move a memory's
// confidence (higher confidence gets injected into future sessions; lower does
// not).
var optimizerMemoryFeedbackCmd = &cobra.Command{
	Use:   "feedback <memory-id> <up|down>",
	Short: "Boost (up) or lower (down) a memory's confidence",
	Args:  cobra.ExactArgs(2),
	RunE:  runOptimizerMemoryFeedback,
}

func runOptimizerMemoryFeedback(_ *cobra.Command, args []string) error {
	id, signal := args[0], strings.ToLower(args[1])
	if signal != optimizer.SignalUp && signal != optimizer.SignalDown {
		return fmt.Errorf("signal must be 'up' or 'down', got %q", signal)
	}
	store, err := optimizer.NewFileMemoryStore(firstNonEmpty(optimizerMemoryStore, defaultMemoryStore))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if err := store.RecordFeedback(id, signal, optimizer.HumanFeedbackWeight, signal == optimizer.SignalUp); err != nil {
		return err
	}
	fmt.Printf("recorded %s for %s\n", signal, id)
	return nil
}

// gitRepoResolver maps a session's working directory to (repo, commit) by
// running git in THAT directory — so memory is scoped to the repo the session
// is actually in, even when one optimizer proxies sessions across many repos.
// Best-effort: on non-git dirs it falls back to the directory basename.
func gitRepoResolver(cwd string) (repo, commit string) {
	if cwd == "" {
		return "", ""
	}
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		repo = filepath.Base(strings.TrimSpace(string(out)))
	}
	if repo == "" {
		repo = filepath.Base(cwd)
	}
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--short", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	return repo, commit
}

// deriveRepoContext best-effort resolves the repo key + current commit for
// grounding episodes (about: resource:repo, code_state.commit). Falls back to
// the working-directory basename when git isn't available.
func deriveRepoContext() (repo, commit string) {
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		repo = filepath.Base(strings.TrimSpace(string(out)))
	}
	if repo == "" {
		if wd, err := os.Getwd(); err == nil {
			repo = filepath.Base(wd)
		} else {
			repo = "local"
		}
	}
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	return repo, commit
}

// optimizerSetup is a configured server plus the metadata the banner needs and
// an idempotent cleanup that closes the compressor / file reporter.
type optimizerSetup struct {
	srv           *optimizer.Server
	cleanup       func()
	listen        string
	upstream      string
	telemetryURL  string
	usageLog      string
	compressStore string // resolved durable path, or "" when ephemeral
}

// resolveListen resolves the bind address from flag / env / default.
func resolveListen() string {
	return firstNonEmpty(optimizerListen, os.Getenv("FORGE_OPTIMIZER_LISTEN"), optimizer.DefaultListen)
}

// buildOptimizerSetup wires reporters + optional compressor and constructs the
// server, shared by `optimizer` and `optimizer claude`.
func buildOptimizerSetup(logger *slog.Logger) (*optimizerSetup, error) {
	listen := resolveListen()

	// Upstream resolution — shared precedence (see resolveOptimizerUpstream):
	// managed settings > --upstream > $FORGE_OPTIMIZER_UPSTREAM >
	// ~/.forge/settings.json > ANTHROPIC_BASE_URL (gateway chaining) > default.
	// Reading ANTHROPIC_BASE_URL lets the optimizer sit in front of an org
	// gateway. (In `claude` mode we read it BEFORE overriding the child's
	// ANTHROPIC_BASE_URL, so chaining still works.)
	upstream := resolveOptimizerUpstream(optimizerUpstream, os.Getenv("ANTHROPIC_BASE_URL"), listen)
	if upstream == "" {
		upstream = optimizer.DefaultUpstream // nothing configured
	} else if err := validateUpstream(upstream, listen); err != nil {
		return nil, fmt.Errorf("optimizer upstream: %w", err)
	}

	var closers []func()
	cleanup := func() {
		for _, c := range closers {
			c()
		}
		closers = nil
	}

	reporters := optimizer.MultiReporter{optimizer.LogReporter{Logger: logger}}
	usageLog := optimizerUsageLog
	if usageLog != "" {
		fr, err := optimizer.NewFileReporter(usageLog)
		if err != nil {
			return nil, err
		}
		closers = append(closers, func() { _ = fr.Close() })
		reporters = append(reporters, fr)
	}
	telemetryURL := firstNonEmpty(optimizerTelemetry, os.Getenv("FORGE_OPTIMIZER_TELEMETRY_URL"))
	if telemetryURL != "" {
		reporters = append(reporters, optimizer.ControlPlaneReporter{
			Endpoint:    telemetryURL,
			OrgID:       os.Getenv("FORGE_ORG_ID"),
			WorkspaceID: os.Getenv("FORGE_WORKSPACE_ID"),
			Token:       os.Getenv("FORGE_PLATFORM_TOKEN"),
			Logger:      logger,
		})
	}

	var compressor *optimizer.Compressor
	compressStore := optimizerCompressStore // resolved path shown in the banner
	if optimizerCompress {
		// Ephemeral by default: no --compress-store → a per-process temp bbolt
		// file, removed on shutdown. It is only a retrieval cache, so nothing of
		// value is lost between runs (see the comment on the default above).
		var ephemeralDir string
		if compressStore == "" {
			dir, err := os.MkdirTemp("", "forge-optimizer-ctxzip-")
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("creating ephemeral compress store: %w", err)
			}
			ephemeralDir = dir
			compressStore = filepath.Join(dir, "ctxzip.db")
		}

		c, err := optimizer.NewCompressor(optimizer.CompressConfig{
			StorePath:     compressStore,
			MinTokens:     optimizerCompressMinT,
			ProtectRecent: optimizerCompressProtect,
		})
		if err != nil {
			if ephemeralDir != "" {
				_ = os.RemoveAll(ephemeralDir)
			}
			cleanup()
			return nil, err
		}
		compressor = c
		// Close the bbolt handle first, then remove the temp dir (order matters).
		closers = append(closers, func() { _ = c.Close() })
		if ephemeralDir != "" {
			closers = append(closers, func() { _ = os.RemoveAll(ephemeralDir) })
			compressStore = "" // signal "ephemeral" to the banner
		}
		if optimizerInbandExpand {
			logger.Info("compression enabled — context_expand resolved in-band (zero-config; no MCP registration needed)")
		} else {
			logger.Info("compression enabled — context_expand served at " + listen + "/mcp; register it with Claude Code (see banner)")
		}
	}

	var memory *optimizer.MemoryFormer
	if optimizerMemory {
		store, err := optimizer.NewFileMemoryStore(firstNonEmpty(optimizerMemoryStore, defaultMemoryStore))
		if err != nil {
			cleanup()
			return nil, err
		}
		closers = append(closers, func() { _ = store.Close() })
		repo, commit := deriveRepoContext()
		memory = optimizer.NewMemoryFormer(optimizer.MemoryFormerConfig{
			Store:        store,
			Repo:         repo,   // fallback only (when a request has no working dir)
			Commit:       commit, // fallback only
			RepoResolver: gitRepoResolver,
			Logger:       logger,
			Recall:       optimizer.RecallConfig{Enabled: optimizerMemoryRecall, MinConfidence: optimizerMemoryMinConf},
			Consolidate:  optimizerMemoryConsolidate,
		})
		logger.Info("memory formation enabled — episodes distilled off the wire",
			"repo", repo, "recall", optimizerMemoryRecall, "consolidate", optimizerMemoryConsolidate,
			"store", firstNonEmpty(optimizerMemoryStore, defaultMemoryStore))
	}

	srv, err := optimizer.New(optimizer.Config{
		Listen:       listen,
		Upstream:     upstream,
		Reporter:     reporters,
		Compressor:   compressor,
		InbandExpand: optimizerInbandExpand,
		Memory:       memory,
		Logger:       logger,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	return &optimizerSetup{
		srv: srv, cleanup: cleanup, listen: listen,
		upstream: srv.Upstream(), telemetryURL: telemetryURL, usageLog: usageLog,
		compressStore: compressStore,
	}, nil
}

func (st *optimizerSetup) printBanner() {
	if optimizerQuietBanner {
		return
	}
	compressState := "off"
	if optimizerCompress {
		mode := "in-band retrieval"
		if !optimizerInbandExpand {
			mode = "MCP retrieval"
		}
		storeDesc := st.compressStore
		if storeDesc == "" {
			storeDesc = "(ephemeral, per-process — removed on exit)"
		}
		compressState = "ON (experimental, " + mode + ") → " + storeDesc
	}
	needsMCP := optimizerCompress && !optimizerInbandExpand
	fmt.Fprint(os.Stderr, banner(st.listen, st.upstream, st.telemetryURL, st.usageLog, compressState, needsMCP))
}

func runOptimizer(cmd *cobra.Command, _ []string) error {
	if optimizerPrintSetup {
		fmt.Print(setupSnippet(resolveListen()))
		return nil
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	setup, err := buildOptimizerSetup(logger)
	if err != nil {
		return err
	}
	defer setup.cleanup()
	setup.printBanner()

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return setup.srv.Run(ctx)
}

// runOptimizerClaude launches `claude` routed through the optimizer (like
// `headroom wrap claude`). If a forge optimizer is already listening on the
// target address it ATTACHES to it (shared proxy); otherwise it starts one
// in-process for the lifetime of this Claude Code session.
func runOptimizerClaude(cmd *cobra.Command, args []string) error {
	// Compression is the point of wrapping, so default it ON unless the user
	// explicitly set --compress. (Ignored in attach mode — see below.)
	if !cmd.Flags().Changed("compress") {
		optimizerCompress = true
	}
	// Memory formation is likewise valuable by default when wrapping a real
	// session (it needs a repo context, which wrap mode has).
	if !cmd.Flags().Changed("memory") {
		optimizerMemory = true
	}

	// Locate claude before we start anything, so we fail fast with guidance.
	claudeBin := firstNonEmpty(optimizerClaudeBin, "claude")
	resolved, lookErr := exec.LookPath(claudeBin)
	if lookErr != nil {
		return fmt.Errorf("could not find %q on PATH — install Claude Code or pass --claude-bin: %w", claudeBin, lookErr)
	}

	// In wrap mode the child claude inherits our terminal, so the optimizer's
	// own logs (startup + per-request lines) must go to a FILE — otherwise they
	// bleed into Claude Code's TUI.
	logger, logClose, logPath := newWrapLogger()
	defer logClose()
	listen := resolveListen()

	// Attach path: reuse an already-running forge optimizer on this address.
	switch running, ours := probeExistingOptimizer(listen); {
	case running && !ours:
		return fmt.Errorf("address %s is already in use by a non-optimizer service; choose another with --listen", listen)
	case running && ours:
		if compressionFlagsChanged(cmd) {
			logger.Warn("attaching to an existing optimizer — its settings apply; --compress* flags on this command are ignored")
		}
		logger.Info("attaching to existing forge optimizer", "base_url", "http://"+listen, "claude", resolved)
		// Ensure context_expand is registered against the shared proxy. We leave
		// it registered on exit — the proxy (and its /mcp endpoint) outlives this
		// session, and other attached sessions may still need it.
		if !optimizerInbandExpand {
			registerOptimizerMCP(resolved, listen, logger)
		}
		printWrapNotice(listen, logPath, true, !optimizerInbandExpand)
		return launchClaudeChild(resolved, args, listen, func() {})
	}

	// Own path: no proxy running — start one in-process for this session.
	setup, err := buildOptimizerSetup(logger)
	if err != nil {
		return err
	}
	defer setup.cleanup()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	go func() {
		if runErr := setup.srv.Run(ctx); runErr != nil {
			logger.Error("optimizer server stopped", "error", runErr.Error())
		}
	}()
	if err := waitListenReady(setup.listen, 5*time.Second); err != nil {
		return fmt.Errorf("optimizer did not become ready on %s: %w", setup.listen, err)
	}

	logger.Info("launching Claude Code through the optimizer", "claude", resolved, "base_url", "http://"+setup.listen)
	// Auto-register context_expand over MCP (Headroom-style) so retrieval rides
	// Claude Code's tool loop and responses stream through untouched. We own this
	// proxy, so de-register when the session ends.
	deregister := func() {}
	mcp := optimizerCompress && !optimizerInbandExpand
	if mcp {
		deregister = registerOptimizerMCP(resolved, setup.listen, logger)
	}
	printWrapNotice(setup.listen, logPath, false, mcp)
	return launchClaudeChild(resolved, args, setup.listen, func() {
		deregister()
		cancel()
		setup.cleanup()
	})
}

// newWrapLogger returns a logger that writes to .forge/optimizer.log (so the
// wrapped claude's TUI stays clean), falling back to stderr if the file can't
// be opened. Returns the logger, a close func, and the resolved log path ("" on
// fallback).
func newWrapLogger() (*slog.Logger, func(), string) {
	path := forgeFile("optimizer.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		if f, ferr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
			l := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
			return l, func() { _ = f.Close() }, path
		}
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})), func() {}, ""
}

// printWrapNotice prints a one-time human-readable line to stderr BEFORE claude
// takes over the terminal — enough to tell the user what's happening and where
// to look, without the ongoing per-request log spam.
func printWrapNotice(listen, logPath string, attach, mcp bool) {
	if optimizerQuietBanner {
		return
	}
	verb := "starting proxy"
	if attach {
		verb = "attaching to running proxy"
	}
	fmt.Fprintf(os.Stderr, "\nforge optimizer: %s at http://%s — routing Claude Code through it\n", verb, listen)
	if mcp {
		fmt.Fprintf(os.Stderr, "  context_expand → auto-registered with Claude Code (MCP); responses stream normally\n")
	}
	if logPath != "" {
		fmt.Fprintf(os.Stderr, "  logs → %s    stats → http://%s/stats\n\n", logPath, listen)
	} else {
		fmt.Fprintf(os.Stderr, "  stats → http://%s/stats\n\n", listen)
	}
}

// launchClaudeChild runs claude with ANTHROPIC_BASE_URL pointed at listen,
// inheriting the terminal. On exit it runs beforeExit (proxy teardown for the
// own path; a no-op for attach) and propagates claude's status code.
func launchClaudeChild(resolved string, args []string, listen string, beforeExit func()) error {
	child := exec.Command(resolved, args...) //nolint:gosec // user-invoked wrap of their own claude binary
	child.Env = childEnvForClaude(listen)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := child.Run()

	beforeExit()
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	return runErr
}

// mcpServerName is how the optimizer's context_expand endpoint is registered
// with Claude Code (the same name the banner prints for manual setup).
const mcpServerName = "forge-optimizer"

// registerOptimizerMCP registers the optimizer's /mcp endpoint with Claude Code
// at user scope, so context_expand works with zero manual setup — the same move
// Headroom makes for its retrieve tool. Retrieval then rides Claude Code's
// normal tool loop (a client-side MCP round-trip), which is why the proxy never
// has to buffer or de-stream a response to catch expansions.
//
// It drops any stale entry of this name first so the URL always matches this
// run's port, then adds ours. Everything is best-effort: a failure only means
// the user would add it manually (logged with the exact command), never fatal.
// Returns a de-register func for the caller to run on exit when it owns the
// proxy lifecycle.
func registerOptimizerMCP(claudeBin, listen string, logger *slog.Logger) func() {
	url := "http://" + listen + "/mcp"
	_ = exec.Command(claudeBin, "mcp", "remove", mcpServerName, "--scope", "user").Run()                       //nolint:gosec // fixed args, user's own claude
	add := exec.Command(claudeBin, "mcp", "add", "--transport", "http", "--scope", "user", mcpServerName, url) //nolint:gosec // fixed args
	if out, err := add.CombinedOutput(); err != nil {
		logger.Warn("could not auto-register context_expand with Claude Code — expansion unavailable unless added manually",
			"error", strings.TrimSpace(string(out)),
			"manual", "claude mcp add --transport http "+mcpServerName+" "+url)
		return func() {}
	}
	logger.Info("registered context_expand with Claude Code (MCP, user scope)", "server", mcpServerName, "url", url)
	return func() {
		if err := exec.Command(claudeBin, "mcp", "remove", mcpServerName, "--scope", "user").Run(); err != nil { //nolint:gosec // fixed args
			logger.Debug("could not de-register optimizer MCP", "error", err.Error())
		}
	}
}

// probeExistingOptimizer reports whether something is listening on addr and, if
// so, whether it is a forge optimizer (per its /healthz identity).
func probeExistingOptimizer(addr string) (running, ours bool) {
	c := &http.Client{Timeout: 600 * time.Millisecond}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return false, false // nothing accepting connections here
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return true, false
	}
	var h struct {
		Service string `json:"service"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(body, &h)
	return true, h.Service == optimizer.ServiceName
}

// compressionFlagsChanged reports whether the user set any compression-related
// flag — used to warn that they are ignored when attaching.
func compressionFlagsChanged(cmd *cobra.Command) bool {
	for _, name := range []string{"compress", "compress-store", "compress-min-tokens", "compress-protect-recent", "inband-expand", "upstream"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// childEnvForClaude returns the parent environment with ANTHROPIC_BASE_URL
// pointed at the local optimizer and Claude Code's tool-search deferral turned
// on (so a custom base URL doesn't force every MCP/system tool schema into
// context — Claude Code GH #746). ENABLE_TOOL_SEARCH is only set if the user
// hasn't already chosen a value.
func childEnvForClaude(listen string) []string {
	base := "http://" + listen
	seenToolSearch := false
	out := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
			continue // replaced below
		}
		if strings.HasPrefix(kv, "ENABLE_TOOL_SEARCH=") {
			seenToolSearch = true
		}
		out = append(out, kv)
	}
	out = append(out, "ANTHROPIC_BASE_URL="+base)
	if !seenToolSearch {
		out = append(out, "ENABLE_TOOL_SEARCH=true")
	}
	return out
}

// waitListenReady blocks until the address accepts a TCP connection or timeout.
func waitListenReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func setupSnippet(listen string) string {
	base := "http://" + listen
	return fmt.Sprintf(`# Point Claude Code at the forge optimizer:
export ANTHROPIC_BASE_URL=%s
claude
`, base)
}

func banner(listen, upstream, telemetry, usageLog, compressState string, needsMCP bool) string {
	base := "http://" + listen
	tel := telemetry
	if tel == "" {
		tel = "(none — local only)"
	}
	ulog := usageLog
	if ulog == "" {
		ulog = "(disabled)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nforge optimizer — context optimizer for Claude Code\n")
	fmt.Fprintf(&b, "  listening   : %s\n", base)
	fmt.Fprintf(&b, "  upstream    : %s\n", upstream)
	fmt.Fprintf(&b, "  compression : %s\n", compressState)
	fmt.Fprintf(&b, "  telemetry   : %s\n", tel)
	fmt.Fprintf(&b, "  usage log   : %s\n\n", ulog)
	fmt.Fprintf(&b, "Point Claude Code at it (new shell):\n")
	fmt.Fprintf(&b, "  export ANTHROPIC_BASE_URL=%s\n", base)
	if needsMCP {
		fmt.Fprintf(&b, "  claude mcp add --transport http forge-optimizer %s/mcp   # enables context_expand\n", base)
	}
	fmt.Fprintf(&b, "  claude\n\n")
	fmt.Fprintf(&b, "See usage locally:\n")
	fmt.Fprintf(&b, "  curl -s %s/stats | jq         # running totals + recent requests\n", base)
	if usageLog != "" {
		fmt.Fprintf(&b, "  tail -f %s   # per-request NDJSON\n", usageLog)
	}
	fmt.Fprintf(&b, "\n")
	return b.String()
}
