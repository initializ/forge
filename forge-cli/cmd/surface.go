package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/initializ/forge/forge-cli/internal/surface"
	"github.com/initializ/forge/forge-cli/internal/tryview"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// runSurface is the bare-`forge` entrypoint (rootCmd.RunE). It greets the user,
// then lets them either shell into a coding agent (Claude Code) wired with forge
// knowledge + tools, or use forge's own in-process builder agent.
func runSurface(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	// Non-interactive (CI, piped): there's nothing to drive — show help and exit.
	if !interactive {
		return cmd.Help()
	}
	color := term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""

	surface.PrintIntro(out, color)

	claudeBin, claudeErr := exec.LookPath(firstNonEmpty(optimizerClaudeBin, "claude"))
	agent, ok, err := surface.ChooseAgent(os.Stdin, out, claudeErr == nil)
	if err != nil {
		return err
	}
	if !ok {
		return nil // canceled
	}

	switch agent {
	case surface.ChoiceClaude:
		if claudeErr != nil {
			_, _ = fmt.Fprintf(out, "  Claude Code isn't on your PATH — using forge native instead.\n\n")
			return runNativeSurface(cmd, color)
		}
		useOpt, ok, err := surface.ChooseOptimizer(os.Stdin, out, color)
		if err != nil || !ok {
			return err
		}
		return launchClaudeSurface(cmd, out, claudeBin, useOpt, color)
	default:
		return runNativeSurface(cmd, color)
	}
}

// forgeMCPName is how the forge tool server is registered with Claude Code.
const forgeMCPName = "forge"

// launchClaudeSurface registers the forge MCP server DURABLY with Claude Code
// (a user-scope stdio server, so it survives across sessions — including a
// direct `claude` resume, just like the optimizer keeps context_expand), then
// execs claude inheriting the terminal, optionally routed through the optimizer.
func launchClaudeSurface(cmd *cobra.Command, out io.Writer, claudeBin string, useOptimizer, color bool) error {
	// Durable registration: Claude Code spawns `forge mcp-serve` over stdio every
	// session, so forge_docs + forge-ops stay available on resume. Left registered
	// on exit (remove with `claude mcp remove forge`).
	registerForgeMCP(out, claudeBin)

	if !useOptimizer {
		// Plain Claude Code. If an optimizer is already active system-wide (a
		// daemon wired claude settings), say so rather than silently routing.
		if running, ours := probeExistingOptimizer(resolveListen()); running && ours {
			_, _ = fmt.Fprintf(os.Stderr, "  Note: a forge optimizer is running at %s and may be wired into Claude Code globally.\n"+
				"        Your session will use it. Run `forge optimizer stop` to disable it.\n\n", resolveListen())
		}
		surface.PrintLaunching(out, color, "launching Claude Code with forge wired in…")
		return execClaude(claudeBin, nil, os.Environ(), nil)
	}

	// Optimizer on: obtain a proxy (adopt an existing one, else start our own with
	// in-band expansion so there is NO second MCP server), then route claude
	// through it via ANTHROPIC_BASE_URL. Teardown is threaded through execClaude's
	// cleanup so it runs even when claude exits non-zero (Ctrl-C = 130) — os.Exit
	// would otherwise skip deferred cleanup (leaking the ctxzip temp store and
	// dropping buffered memory/usage writes).
	logger, logClose, _ := newWrapLogger()
	listen := resolveListen()

	switch running, ours := probeExistingOptimizer(listen); {
	case running && !ours:
		logClose()
		return fmt.Errorf("address %s is already in use by a non-optimizer service; stop it or choose another optimizer --listen", listen)
	case running && ours:
		logger.Info("attaching to existing forge optimizer", "base_url", "http://"+listen)
		printOptimizerDashboardNote(out, color)
		surface.PrintLaunching(out, color, "launching Claude Code through your running optimizer, with forge wired in…")
		return execClaude(claudeBin, nil, childEnvForClaude(listen), logClose)
	}

	// Own path: start the optimizer in-process with compression + memory + in-band
	// expansion (so context_expand needs no second MCP server).
	optimizerCompress = true
	optimizerMemory = true
	optimizerInbandExpand = true
	setup, err := buildOptimizerSetup(logger)
	if err != nil {
		logClose()
		return fmt.Errorf("starting optimizer: %w", err)
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	go func() {
		if runErr := setup.srv.Run(ctx); runErr != nil {
			logger.Error("optimizer server stopped", "error", runErr.Error())
		}
	}()
	cleanup := func() { cancel(); setup.cleanup(); logClose() }
	if err := waitListenReady(setup.listen, 5*time.Second); err != nil {
		cleanup()
		return fmt.Errorf("optimizer did not become ready on %s: %w", setup.listen, err)
	}
	printOptimizerDashboardNote(out, color)
	surface.PrintLaunching(out, color, "launching Claude Code through the forge optimizer, with forge wired in…")
	return execClaude(claudeBin, nil, childEnvForClaude(setup.listen), cleanup)
}

// registerForgeMCP registers the forge stdio MCP server with Claude Code at user
// scope (idempotent: remove-then-add), pointing at `forge mcp-serve`. Best-effort
// — a failure just means the user adds it manually (the exact command is printed).
func registerForgeMCP(out io.Writer, claudeBin string) {
	forgeBin, err := os.Executable()
	if err != nil {
		forgeBin = "forge"
	}
	_ = exec.Command(claudeBin, "mcp", "remove", forgeMCPName, "--scope", "user").Run()                        //nolint:gosec // fixed args
	add := exec.Command(claudeBin, "mcp", "add", "--scope", "user", forgeMCPName, "--", forgeBin, "mcp-serve") //nolint:gosec // fixed args
	if o, addErr := add.CombinedOutput(); addErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "  Warning: could not register forge tools with Claude Code: %s\n"+
			"    Add manually: claude mcp add --scope user %s -- %q mcp-serve\n\n",
			strings.TrimSpace(string(o)), forgeMCPName, forgeBin)
		return
	}
	_, _ = fmt.Fprintf(out, "  forge tools registered with Claude Code (stays available on resume; remove with `claude mcp remove %s`).\n", forgeMCPName)
}

// printOptimizerDashboardNote points the user at the savings + memory dashboard.
func printOptimizerDashboardNote(out io.Writer, color bool) {
	dim := surface.Dim(color)
	_, _ = fmt.Fprintf(out, "  %s\n", dim("Optimizer on: compressing context (cache-safe, reversible) and forming memory."))
	_, _ = fmt.Fprintf(out, "  %s\n\n", dim("View token savings + memory at http://127.0.0.1:4200/#/optimizer  (run `forge ui`)."))
}

// execClaude runs claude inheriting the terminal, then runs cleanup (if any)
// BEFORE propagating claude's exit code — so a non-zero exit (e.g. Ctrl-C = 130,
// the usual way users quit) doesn't skip optimizer teardown the way a bare
// os.Exit would skip deferred cleanup.
func execClaude(resolved string, args, env []string, cleanup func()) error {
	child := exec.Command(resolved, args...) //nolint:gosec // user-invoked wrap of their own claude binary
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := child.Run()
	if cleanup != nil {
		cleanup()
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	return runErr
}

// runNativeSurface runs forge's own in-process builder agent in a terminal REPL.
func runNativeSurface(cmd *cobra.Command, color bool) error {
	out := cmd.OutOrStdout()

	// Resolve a model credential the same way `forge try` does.
	res, err := resolveTryProvider(cmd.Context(), tryFlags{}, os.Stdin, out, true, color)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "  %s\n\n", res.Label)

	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	sess, err := runtime.NewBuilderSession(cmd.Context(), runtime.BuilderSessionOptions{
		WorkDir:      workDir,
		Provider:     res.Provider,
		Model:        res.Model,
		EnvOverrides: res.EnvOverrides,
		Verbose:      verbose,
	})
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()

	renderer := tryview.New(out, false, false, color)
	sess.AuditLogger().AddSink(renderer)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	_, _ = fmt.Fprintf(out, "  Ask me to build an agent, or a how-to question. /exit to quit.\n")
	return surfaceREPL(ctx, sess, out, color)
}

// surfaceREPL reads one prompt per line and runs a native builder turn each,
// keeping history in the session. Exits on /exit, /quit, Ctrl-D, or Ctrl-C.
func surfaceREPL(ctx context.Context, sess *runtime.BuilderSession, out io.Writer, color bool) error {
	lines := make(chan string)
	eof := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(eof)
	}()

	for {
		_, _ = fmt.Fprint(out, "\nyou › ")
		var pending string
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprintln(out)
			return nil
		case <-eof:
			_, _ = fmt.Fprintln(out)
			return nil
		case line := <-lines:
			pending = strings.TrimSpace(line)
		}
		if pending == "" {
			continue
		}
		if pending == "/exit" || pending == "/quit" {
			return nil
		}
		if pending == "/help" {
			_, _ = fmt.Fprintln(out, "  Ask me to build/modify a forge agent, or any how-to question about forge.\n  Commands: /help, /exit")
			continue
		}
		reply, err := sess.RunTurn(ctx, pending, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			_, _ = fmt.Fprintf(out, "\n  error: %v\n", err)
			continue
		}
		_, _ = fmt.Fprintf(out, "\n%s\n%s\n", surface.Accent(color)("forge ›"), surface.RenderMarkdown(reply, color))
	}
}
