package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/initializ/forge/forge-cli/config"
	"github.com/initializ/forge/forge-cli/internal/tryview"
	"github.com/initializ/forge/forge-cli/runtime"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/types"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// forge try — the instant-gratification onboarding command. It scaffolds a
// keyless demo agent into a throwaway workspace, resolves whatever model
// credential is already available, and drops the user into a streaming chat
// whose tool calls and egress checks render inline. No build, no channels, no
// server, no secrets on disk. See issue #350.
var tryCmd = &cobra.Command{
	Use:   "try [prompt]",
	Short: "Talk to a live agent in your terminal: no build, no cluster",
	Long: "Scaffold a keyless demo agent and chat with it in your terminal, watching every " +
		"tool call and egress check as it runs. The workspace is ephemeral unless you pass " +
		"--keep, which writes it to ./forge-quickstart so you can make it your own.",
	Args:         cobra.MaximumNArgs(1),
	RunE:         runTry,
	SilenceUsage: true, // runtime errors (e.g. no credential) shouldn't dump flag help
}

// tryFlags holds the parsed command flags for one `forge try` invocation.
type tryFlags struct {
	keep     bool
	provider string
	model    string
	once     string
	onceSet  bool // whether --once was explicitly set (distinguishes "" from unset)
	quiet    bool
	audit    bool
	prompt   string // optional positional first prompt
}

func init() {
	tryCmd.Flags().Bool("keep", false, "keep the demo agent in ./forge-quickstart instead of a temp dir")
	tryCmd.Flags().String("provider", "", "model provider: openai, anthropic, gemini, or ollama")
	tryCmd.Flags().String("model", "", "model name (defaults to the provider's default)")
	tryCmd.Flags().String("once", "", "run a single prompt non-interactively, then exit")
	tryCmd.Flags().Bool("quiet", false, "hide the inline tool/egress loop lines")
	tryCmd.Flags().Bool("audit", false, "show the full NDJSON audit event stream")
}

func parseTryFlags(cmd *cobra.Command, args []string) tryFlags {
	f := tryFlags{}
	f.keep, _ = cmd.Flags().GetBool("keep")
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	f.once, _ = cmd.Flags().GetString("once")
	f.onceSet = cmd.Flags().Changed("once")
	f.quiet, _ = cmd.Flags().GetBool("quiet")
	f.audit, _ = cmd.Flags().GetBool("audit")
	if len(args) > 0 {
		f.prompt = args[0]
	}
	return f
}

func runTry(cmd *cobra.Command, args []string) error {
	flags := parseTryFlags(cmd, args)
	out := cmd.OutOrStdout()
	color := term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""
	accent := tryAccent(color)

	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	printTryHeader(out, accent)

	res, err := resolveTryProvider(cmd.Context(), flags, os.Stdin, out, interactive)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "  %s\n", res.Label)

	dir, cleanup, err := tryWorkspace(flags.keep)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	opts := quickstartPreset(res.Provider, res.Model)
	opts.OutputDir = dir
	opts.Force = true // forge owns this dir (a temp dir, or ./forge-quickstart)

	if err := scaffold(opts); err != nil {
		return err
	}

	cfg, err := config.LoadForgeConfig(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		return fmt.Errorf("loading scaffolded agent: %w", err)
	}

	printTrySummary(cmd, cfg, opts)

	sess, err := runtime.NewLocalSession(cmd.Context(), runtime.LocalSessionOptions{
		Config:       cfg,
		WorkDir:      dir,
		EnvOverrides: res.EnvOverrides,
		Verbose:      verbose, // global -v/--verbose; un-silences the runner logger
	})
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()

	// Visible-loop renderer: inline tool/egress/guardrail lines from the
	// agent's own audit stream. --quiet hides it; --audit shows full NDJSON.
	renderer := tryview.New(out, flags.quiet, flags.audit, color)
	sess.AuditLogger().AddSink(renderer)

	// Ctrl-C cancels the in-flight turn (executor cancels at the next
	// iteration / tool boundary) and ends the loop.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	// --once: run a single turn (or, for an empty prompt, just load + exit).
	if flags.onceSet {
		if strings.TrimSpace(flags.once) != "" {
			if err := tryOneTurn(ctx, sess, renderer, out, flags.once); err != nil {
				return err
			}
		}
		if flags.keep {
			_, _ = fmt.Fprintf(out, "\n  Kept the demo agent in %s\n", accent("./forge-quickstart"))
		}
		return nil
	}

	printTrySuggestions(out)
	if err := tryREPL(ctx, sess, renderer, cmd, flags); err != nil {
		return err
	}
	printTryGraduation(out, accent, flags.keep)
	return nil
}

// tryOneTurn runs a single non-interactive turn and prints the reply, then the
// compact audit summary.
func tryOneTurn(ctx context.Context, sess *runtime.LocalSession, renderer *tryview.Renderer, out io.Writer, prompt string) error {
	reply, err := sess.RunTurn(ctx, prompt, nil)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "\nagent › %s\n", reply)
	renderer.FlushSummary()
	return nil
}

// tryREPL reads one prompt per line and runs a turn each, keeping history in
// the session. It exits on /exit, /quit, Ctrl-D (EOF), or a cancelled context.
// An optional positional prompt seeds the first turn. The renderer prints the
// inline loop (via the audit sink) during each turn; the compact summary
// prints after the reply.
func tryREPL(ctx context.Context, sess *runtime.LocalSession, renderer *tryview.Renderer, cmd *cobra.Command, flags tryFlags) error {
	out := cmd.OutOrStdout()

	// Read stdin on a goroutine so the prompt is cancellable: a blocking
	// scanner.Scan() can't observe ctx, so Ctrl-C (via signal.NotifyContext)
	// would otherwise hang at "you ›" until Enter. The goroutine leaks at most
	// one pending read when we return, which is harmless as the process exits.
	lines := make(chan string)
	eof := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(cmd.InOrStdin())
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(eof)
	}()

	pending := strings.TrimSpace(flags.prompt)
	for {
		if pending == "" {
			_, _ = fmt.Fprint(out, "\nyou › ")
			select {
			case <-ctx.Done():
				_, _ = fmt.Fprintln(out)
				return nil // Ctrl-C
			case <-eof:
				return nil // Ctrl-D / EOF
			case line := <-lines:
				pending = strings.TrimSpace(line)
			}
		}
		if pending == "" {
			continue
		}
		if pending == "/exit" || pending == "/quit" {
			break
		}
		reply, err := sess.RunTurn(ctx, pending, nil)
		pending = ""
		if err != nil {
			if ctx.Err() != nil {
				break // cancelled mid-turn
			}
			_, _ = fmt.Fprintf(out, "\n  error: %v\n", err)
			continue
		}
		_, _ = fmt.Fprintf(out, "\nagent › %s\n", reply)
		renderer.FlushSummary()
	}
	return nil
}

// tryWorkspace returns the scaffold target directory, plus a cleanup func for
// the ephemeral case (nil under --keep). The ephemeral agent lives in a leaf
// under a fresh temp dir so scaffold()'s not-exists check still holds, and the
// whole temp tree is removed on exit — no secrets ever persist.
func tryWorkspace(keep bool) (dir string, cleanup func(), err error) {
	if keep {
		return filepath.Join(".", "forge-quickstart"), nil, nil
	}
	base, err := os.MkdirTemp("", "forge-try-")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp workspace: %w", err)
	}
	return filepath.Join(base, "quickstart"), func() { _ = os.RemoveAll(base) }, nil
}

// quickstartPreset is the demo agent: the native forge LLM executor, the
// keyless weather skill, and a few safe builtins. Egress is auto-derived from
// the skill (allowlist, no dev-open); no channels, no long-term memory, no
// secrets. Preset stops scaffold() before its auto-run so `forge try` drives
// the loop in-process.
func quickstartPreset(provider, model string) *initOptions {
	return &initOptions{
		Name:           "quickstart",
		AgentID:        "quickstart",
		Framework:      "forge",
		ModelProvider:  provider,
		CustomModel:    model, // empty → provider default (buildTemplateData)
		AuthMethod:     "apikey",
		BuiltinTools:   []string{"http_request", "datetime_now", "math_calculate"},
		Skills:         []string{"weather"},
		NonInteractive: true,
		Preset:         true,
		EnvVars:        map[string]string{},
	}
}

// printTrySummary prints the one-line agent summary shown at startup, e.g.
//
//	Agent: quickstart · skills: weather · tools: http_request, datetime_now, math_calculate
func printTrySummary(cmd *cobra.Command, cfg *types.ForgeConfig, opts *initOptions) {
	out := cmd.OutOrStdout()
	parts := []string{fmt.Sprintf("Agent: %s", cfg.AgentID)}
	if len(opts.Skills) > 0 {
		parts = append(parts, "skills: "+strings.Join(opts.Skills, ", "))
	}
	if len(opts.BuiltinTools) > 0 {
		parts = append(parts, "tools: "+strings.Join(opts.BuiltinTools, ", "))
	}
	_, _ = fmt.Fprintf(out, "  %s\n", strings.Join(parts, " · "))
}

// trySuggestions are the starter prompts shown before the interactive REPL.
// All three stay within the keyless egress allowlist (weather via wttr.in,
// plus the math + time builtins), so none dead-ends on a blocked domain.
var trySuggestions = []string{
	"what's the weather in Tokyo?",
	"what's 17% of 4,200?",
	"what time is it in UTC?",
}

// printTryHeader prints the two-line intro banner.
func printTryHeader(out io.Writer, accent func(string) string) {
	_, _ = fmt.Fprintf(out, "\n  %s: talking to a live agent in your terminal.\n", accent("forge try"))
	_, _ = fmt.Fprintf(out, "  No build, no cluster. Ctrl-D or /exit to quit.\n\n")
}

// printTrySuggestions lists the starter prompts.
func printTrySuggestions(out io.Writer) {
	_, _ = fmt.Fprintf(out, "\n  Try:  %s\n", trySuggestions[0])
	for _, s := range trySuggestions[1:] {
		_, _ = fmt.Fprintf(out, "        %s\n", s)
	}
}

// printTryGraduation frames the delight on exit and points the user up the
// ladder — keep the demo, run it as a service, or package it.
func printTryGraduation(out io.Writer, accent func(string) string, kept bool) {
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "  You just ran an agent whose every tool call and egress you can see and audit.")
	if kept {
		_, _ = fmt.Fprintf(out, "  It's yours in %s: edit skills/, run it with %s, or deploy with %s.\n",
			accent("./forge-quickstart"), accent("forge serve"), accent("forge package"))
		return
	}
	_, _ = fmt.Fprintf(out, "  Want to keep it and make it yours?  ->  %s   (writes ./forge-quickstart)\n", accent("forge try --keep"))
	_, _ = fmt.Fprintf(out, "  Then edit skills/, run it as a service with %s, or deploy with %s.\n",
		accent("forge serve"), accent("forge package"))
}

// tryAccent returns an orange styler for command tokens, or an identity
// function when color is disabled (non-TTY / NO_COLOR).
func tryAccent(color bool) func(string) string {
	if !color {
		return func(s string) string { return s }
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color("#f97316"))
	return func(s string) string { return st.Render(s) }
}

// tryResolution is the outcome of credential auto-resolution: which provider
// and model to run, a human label for the startup summary, and any credential
// env vars to inject for the in-process run (paste-key picker path only).
type tryResolution struct {
	Provider     string
	Model        string
	Label        string
	EnvOverrides map[string]string
}

// errNoTryCredential is returned when no model credential is available and the
// session is non-interactive (CI), so no picker can be shown.
var errNoTryCredential = errors.New(
	"no model credential found. Do one of:\n" +
		"  - set ANTHROPIC_API_KEY, OPENAI_API_KEY, or GEMINI_API_KEY in your environment\n" +
		"  - run `forge try` in an interactive terminal to sign in with OpenAI\n" +
		"  - start a local model with Ollama (https://ollama.com), then `ollama serve`\n" +
		"or pass --provider and --model explicitly")

// resolveTryProvider picks the fastest available model credential with no
// prompts when one exists, and offers a minimal picker only when nothing is.
// Ordering: explicit flags -> env key -> saved OpenAI OAuth -> local Ollama ->
// interactive picker (TTY) -> actionable error (no TTY). It does not add a new
// credential store; it layers an ordering policy over the runner's existing
// resolution.
func resolveTryProvider(ctx context.Context, flags tryFlags, in io.Reader, out io.Writer, interactive bool) (tryResolution, error) {
	// 1. Explicit flags win.
	if flags.provider != "" {
		model := modelOrDefault(flags.provider, flags.model)
		return tryResolution{
			Provider: flags.provider,
			Model:    model,
			Label:    fmt.Sprintf("Using %s (%s).", flags.provider, model),
		}, nil
	}

	// 2. Env key, preferring Anthropic, then OpenAI, then Gemini.
	for _, e := range []struct{ env, provider string }{
		{"ANTHROPIC_API_KEY", "anthropic"},
		{"OPENAI_API_KEY", "openai"},
		{"GEMINI_API_KEY", "gemini"},
	} {
		if os.Getenv(e.env) != "" {
			return tryResolution{
				Provider: e.provider,
				Model:    modelOrDefault(e.provider, flags.model),
				Label:    fmt.Sprintf("Using %s from env.", e.env),
			}, nil
		}
	}

	// 3. Saved OpenAI OAuth session (Responses endpoint).
	if tok, err := oauth.LoadCredentials("openai"); err == nil && tok != nil && tok.RefreshToken != "" {
		return tryResolution{
			Provider: "openai",
			Model:    modelOrDefault("openai", flags.model),
			Label:    "Using OpenAI (signed in).",
		}, nil
	}

	// 4. Local Ollama daemon.
	if ollamaReachable(ctx) {
		model := modelOrDefault("ollama", flags.model)
		return tryResolution{
			Provider: "ollama",
			Model:    model,
			Label:    fmt.Sprintf("Using local Ollama (%s). First response may be slow if the model must download.", model),
		}, nil
	}

	// 5/6. Nothing available.
	if !interactive {
		return tryResolution{}, errNoTryCredential
	}
	return tryPicker(flags, in, out)
}

// modelOrDefault returns the explicit model if set, else the provider default.
func modelOrDefault(provider, model string) string {
	if model != "" {
		return model
	}
	return defaultModelNameForProvider(provider)
}

// ollamaReachable reports whether a local Ollama daemon answers on its default
// port (or OLLAMA_HOST/OLLAMA_BASE_URL host:port). A short dial keeps startup
// snappy when Ollama is absent.
func ollamaReachable(ctx context.Context) bool {
	addr := "127.0.0.1:11434"
	if h := os.Getenv("OLLAMA_HOST"); h != "" {
		addr = strings.TrimPrefix(strings.TrimPrefix(h, "http://"), "https://")
	}
	dctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// tryPicker is the fallback shown only when nothing is auto-detected and a TTY
// is present: sign in with OpenAI, paste a key, or use local Ollama.
func tryPicker(flags tryFlags, in io.Reader, out io.Writer) (tryResolution, error) {
	_, _ = fmt.Fprintln(out, "\n  No model credential found. How would you like to connect?")
	_, _ = fmt.Fprintln(out, "    1) Sign in with OpenAI (recommended)")
	_, _ = fmt.Fprintln(out, "    2) Paste an API key")
	_, _ = fmt.Fprintln(out, "    3) Use local Ollama")
	_, _ = fmt.Fprint(out, "  > ")

	choice, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.TrimSpace(choice) {
	case "1", "":
		if _, err := runOAuthFlow("openai"); err != nil {
			return tryResolution{}, fmt.Errorf("OpenAI sign-in: %w", err)
		}
		return tryResolution{
			Provider: "openai",
			Model:    modelOrDefault("openai", flags.model),
			Label:    "Using OpenAI (signed in).",
		}, nil
	case "2":
		return pasteKeyResolution(flags, out)
	case "3":
		model := modelOrDefault("ollama", flags.model)
		return tryResolution{
			Provider: "ollama",
			Model:    model,
			Label:    fmt.Sprintf("Using local Ollama (%s).", model),
		}, nil
	default:
		return tryResolution{}, fmt.Errorf("unrecognized choice %q", strings.TrimSpace(choice))
	}
}

// pasteKeyResolution prompts for a provider and a masked API key. The key is
// held only in memory for this session (via res.EnvOverrides consumed by
// NewLocalSession) and is never written to disk — not even under --keep, whose
// scaffold env is always empty. A kept agent therefore has no credential on
// disk; a later `forge serve` there needs the env var set by hand.
func pasteKeyResolution(flags tryFlags, out io.Writer) (tryResolution, error) {
	_, _ = fmt.Fprint(out, "  Provider (openai / anthropic / gemini): ")
	prov, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	provider := strings.ToLower(strings.TrimSpace(prov))
	keyEnv, ok := providerKeyEnv[provider]
	if !ok {
		return tryResolution{}, fmt.Errorf("unsupported provider %q for a pasted key", provider)
	}
	_, _ = fmt.Fprint(out, "  API key: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(out)
	if err != nil {
		return tryResolution{}, fmt.Errorf("reading key: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return tryResolution{}, fmt.Errorf("empty API key")
	}
	return tryResolution{
		Provider:     provider,
		Model:        modelOrDefault(provider, flags.model),
		Label:        fmt.Sprintf("Using %s (pasted key).", provider),
		EnvOverrides: map[string]string{keyEnv: key},
	}, nil
}

// providerKeyEnv maps a provider to its API-key environment variable.
var providerKeyEnv = map[string]string{
	"openai":    "OPENAI_API_KEY",
	"anthropic": "ANTHROPIC_API_KEY",
	"gemini":    "GEMINI_API_KEY",
}
