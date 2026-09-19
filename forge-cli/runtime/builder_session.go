package runtime

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/initializ/forge/forge-cli/internal/surface"
	"github.com/initializ/forge/forge-core/a2a"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/types"
)

// BuilderSession is the in-process agent for the native `forge` surface path.
// Unlike LocalSession (forge try) it needs NO forge.yaml — the working dir may
// be empty; the whole point is to CREATE agents there. It assembles the shared
// coreruntime.LLMExecutor with a builder persona, the forge knowledge tool, the
// structured forge-ops, and file + shell tools, then drives turns via RunTurn.
//
// It is a local operator tool: no egress enforcement, no HTTP server, no
// channels. Conversation history lives in memory and never persists.
type BuilderSession struct {
	executor coreruntime.AgentExecutor
	audit    *coreruntime.AuditLogger
	router   *surface.Router
	history  []a2a.Message
	taskID   string
	lastErr  *errBox
}

// BuilderSessionOptions configure a native builder session.
type BuilderSessionOptions struct {
	WorkDir      string
	Provider     string            // resolved provider (e.g. "anthropic")
	Model        string            // resolved model id (may be empty → provider default)
	EnvOverrides map[string]string // credential env from the picker, if any
	Verbose      bool
}

// NewBuilderSession builds the native builder agent. Mirrors NewLocalSession's
// resolve env → model → tools → hooks → executor order, minus egress/channels.
func NewBuilderSession(ctx context.Context, opts BuilderSessionOptions) (*BuilderSession, error) {
	// Synthesize an in-memory config — no forge.yaml on disk is required.
	cfg := &types.ForgeConfig{AgentID: "forge-studio"}
	cfg.Model.Provider = opts.Provider
	cfg.Model.Name = opts.Model

	r, err := NewRunner(RunnerConfig{
		Config:  cfg,
		WorkDir: opts.WorkDir,
		Host:    "127.0.0.1",
		Verbose: opts.Verbose,
		AuditPayloadCapture: coreruntime.AuditPayloadCapture{
			ToolArgs:   true,
			ToolResult: true,
			Redact:     true,
		},
	})
	if err != nil {
		return nil, err
	}
	if !opts.Verbose {
		r.logger = coreruntime.NewJSONLogger(io.Discard, false)
	}

	// Env: process env overlaid with any picker credentials.
	envVars := osEnvMap()
	for k, v := range opts.EnvOverrides {
		envVars[k] = v
		_ = os.Setenv(k, v)
	}

	mc := coreruntime.ResolveModelConfig(cfg, envVars, opts.Provider)
	if mc == nil {
		return nil, fmt.Errorf("no model provider could be resolved for the forge builder")
	}
	r.applyGatewaySettings(ctx, mc)
	r.modelConfig = mc

	llmClient, err := r.buildLLMClient(mc)
	if err != nil {
		return nil, fmt.Errorf("building model client: %w", err)
	}

	// Tools: file read/write/edit/patch + search + shell + forge-ops + forge_docs.
	reg := tools.NewRegistry()
	if err := builtins.RegisterFileTools(reg, opts.WorkDir); err != nil {
		return nil, fmt.Errorf("registering file tools: %w", err)
	}
	if err := builtins.RegisterCodeAgentSearchTools(reg, opts.WorkDir); err != nil {
		return nil, fmt.Errorf("registering search tools: %w", err)
	}
	extra := append([]tools.Tool{surface.NewShellTool(opts.WorkDir), surface.ForgeDocsTool{}},
		surface.ForgeOpsTools(opts.WorkDir)...)
	extra = append(extra, surface.InitializTools(opts.WorkDir)...)
	for _, t := range extra {
		if err := reg.Register(t); err != nil {
			return nil, fmt.Errorf("registering %s: %w", t.Name(), err)
		}
	}

	audit := coreruntime.NewAuditLogger(io.Discard)
	hooks := coreruntime.NewHookRegistry()
	r.registerAuditHooks(hooks, audit)
	r.registerProgressHooks(hooks)

	eb := &errBox{}
	hooks.Register(coreruntime.OnError, func(_ context.Context, hctx *coreruntime.HookContext) error {
		if hctx.Error != nil {
			eb.err = hctx.Error
		}
		return nil
	})

	executor := coreruntime.NewLLMExecutor(coreruntime.LLMExecutorConfig{
		Client:       llmClient,
		Tools:        reg,
		Hooks:        hooks,
		SystemPrompt: surface.BuilderSystemPrompt(),
		ModelName:    mc.Client.Model,
		Provider:     mc.Provider,
	})

	return &BuilderSession{
		executor: executor,
		audit:    audit,
		router:   surface.NewRouter(),
		taskID:   "forge-studio",
		lastErr:  eb,
	}, nil
}

// RunTurn runs one agent turn. It prepends any intent-matched forge knowledge
// (once per session) to the user's input, then executes through the shared
// executor, keeping history in memory.
func (s *BuilderSession) RunTurn(ctx context.Context, prompt string, progress coreruntime.ProgressEmitter) (string, error) {
	if progress != nil {
		ctx = coreruntime.WithProgressEmitter(ctx, progress)
	}

	input := prompt
	if preface := s.router.KnowledgePreface(prompt); preface != "" {
		input = preface + "User request:\n" + prompt
	}

	task := &a2a.Task{ID: s.taskID, History: s.history}
	userMsg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart(input)}}

	s.lastErr.err = nil
	resp, err := s.executor.Execute(ctx, task, userMsg)
	if err != nil {
		if s.lastErr.err != nil {
			return "", s.lastErr.err
		}
		return "", err
	}
	// Persist the ORIGINAL prompt (not the knowledge-prefixed one) so history
	// stays clean; the injected block was reference context for that turn only.
	s.history = append(s.history, a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart(prompt)}})
	if resp != nil {
		s.history = append(s.history, *resp)
	}
	return messageText(resp), nil
}

// AuditLogger exposes the session's audit logger so a renderer can attach.
func (s *BuilderSession) AuditLogger() *coreruntime.AuditLogger { return s.audit }

// Close releases executor resources.
func (s *BuilderSession) Close() error {
	if s.executor != nil {
		return s.executor.Close()
	}
	return nil
}
