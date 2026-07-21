package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/observability"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/types"
)

// LocalSession is an in-process agent runtime for `forge try` (issue #350). It
// assembles the SAME coreruntime.LLMExecutor that `forge run` uses — the
// built-in tool registry, egress enforcement (in-process client + subprocess
// proxy), audit + progress hooks, and provider client — but WITHOUT an HTTP
// server, scheduler, MCP, admission, auth, or long-term memory. There is no
// second executor: this is a trimmed bootstrap around the shared sub-builders.
//
// Turns run one at a time via RunTurn. Conversation history is kept in memory
// and never persisted (the executor Store is nil), so nothing touches disk for
// the ephemeral run.
type LocalSession struct {
	runner       *Runner
	executor     coreruntime.AgentExecutor
	audit        *coreruntime.AuditLogger
	egressClient *http.Client
	proxyStop    func()
	history      []a2a.Message
	taskID       string
}

// LocalSessionOptions configure an in-process `forge try` session.
type LocalSessionOptions struct {
	Config       *types.ForgeConfig
	WorkDir      string
	EnvOverrides map[string]string // credential env from the paste-key picker (else nil)
	Verbose      bool
}

// NewLocalSession builds the in-process executor for the demo agent. The order
// mirrors Run(): resolve env → egress → tools → model → hooks → executor.
func NewLocalSession(ctx context.Context, opts LocalSessionOptions) (*LocalSession, error) {
	if opts.Config == nil {
		return nil, fmt.Errorf("config is required")
	}
	r, err := NewRunner(RunnerConfig{
		Config:  opts.Config,
		WorkDir: opts.WorkDir,
		Host:    "127.0.0.1",
		Verbose: opts.Verbose,
	})
	if err != nil {
		return nil, err
	}
	// Silence the runner's structured logger: `forge try` keeps stdout clean
	// for the chat + the visible-loop renderer (which reads the audit stream,
	// not this logger). Real failures surface as returned errors.
	if !opts.Verbose {
		r.logger = coreruntime.NewJSONLogger(io.Discard, false)
	}

	// envVars: process env, overlaid with the agent's .env, overlaid with any
	// paste-key credentials. The runner's Run() reads only .env; `forge try`
	// also honors credentials already in the environment.
	envVars := osEnvMap()
	fileEnv, _ := LoadEnvFile(filepath.Join(opts.WorkDir, ".env"))
	for k, v := range fileEnv {
		envVars[k] = v
	}
	for k, v := range opts.EnvOverrides {
		envVars[k] = v
		_ = os.Setenv(k, v)
	}

	audit := coreruntime.NewAuditLoggerFromConfig(r.cfg.AuditExport)

	// Resolve the model first — registerSkillTools reads r.modelConfig.
	mc := coreruntime.ResolveModelConfig(opts.Config, envVars, r.cfg.ProviderOverride)
	if mc == nil {
		return nil, fmt.Errorf("no model provider could be resolved for the demo agent")
	}
	r.modelConfig = mc

	// Egress: in-process enforced client (for builtin http tools) + a local
	// proxy for subprocess skills. Both emit egress_allowed / egress_blocked.
	egressClient, proxyURL, proxyStop := r.buildTryEgress(ctx, envVars, audit)

	// Tool registry: builtins + the vendored skills (subprocess via proxy).
	reg := tools.NewRegistry()
	if err := builtins.RegisterAll(reg, builtins.Options{}); err != nil {
		proxyStop()
		return nil, fmt.Errorf("registering builtin tools: %w", err)
	}
	r.registerSkillTools(reg, proxyURL)

	llmClient, err := r.buildLLMClient(mc)
	if err != nil {
		proxyStop()
		return nil, fmt.Errorf("building model client: %w", err)
	}

	hooks := coreruntime.NewHookRegistry()
	r.registerAuditHooks(hooks, audit)
	r.registerProgressHooks(hooks)

	executor := coreruntime.NewLLMExecutor(coreruntime.LLMExecutorConfig{
		Client:       llmClient,
		Tools:        reg,
		Hooks:        hooks,
		SystemPrompt: r.buildSystemPrompt(),
		ModelName:    mc.Client.Model,
		Provider:     mc.Provider,
		// Store is nil: history rides in task.History, nothing persists.
	})

	return &LocalSession{
		runner:       r,
		executor:     executor,
		audit:        audit,
		egressClient: egressClient,
		proxyStop:    proxyStop,
		taskID:       "forge-try",
	}, nil
}

// RunTurn runs exactly one agent turn: the prompt plus the accumulated history,
// through the shared executor. It installs the egress-enforced client and the
// optional progress emitter on the context, appends the user + agent messages
// to history, and returns the agent's text reply.
func (s *LocalSession) RunTurn(ctx context.Context, prompt string, progress coreruntime.ProgressEmitter) (string, error) {
	ctx = security.WithEgressClient(ctx, s.egressClient)
	if progress != nil {
		ctx = coreruntime.WithProgressEmitter(ctx, progress)
	}
	task := &a2a.Task{ID: s.taskID, History: s.history}
	userMsg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []a2a.Part{a2a.NewTextPart(prompt)}}

	resp, err := s.executor.Execute(ctx, task, userMsg)
	if err != nil {
		return "", err
	}
	s.history = append(s.history, *userMsg)
	if resp != nil {
		s.history = append(s.history, *resp)
	}
	return messageText(resp), nil
}

// AuditLogger exposes the session's audit logger so the visible-loop renderer
// (Phase 4) can attach itself as an additional sink.
func (s *LocalSession) AuditLogger() *coreruntime.AuditLogger { return s.audit }

// Close stops the egress proxy and releases executor resources.
func (s *LocalSession) Close() error {
	if s.proxyStop != nil {
		s.proxyStop()
	}
	if s.executor != nil {
		return s.executor.Close()
	}
	return nil
}

// buildTryEgress mirrors Run()'s egress setup, trimmed to what the demo needs:
// the forge.yaml allowlist + skill-derived + LLM-provider domains, an
// in-process enforced client, and (outside a container, non-dev-open) a local
// proxy for subprocess skills. Both the enforcer and the proxy emit
// egress_allowed / egress_blocked audit events, exactly like the server path.
func (r *Runner) buildTryEgress(ctx context.Context, envVars map[string]string, audit *coreruntime.AuditLogger) (*http.Client, string, func()) {
	noop := func() {}

	var domains []string
	for _, d := range security.EffectiveEgressAllowlist(r.cfg.Config, nil) {
		domains = append(domains, expandEgressDomains(d, envVars)...)
	}
	if r.derivedCLIConfig != nil {
		for _, d := range r.derivedCLIConfig.EgressDomains {
			domains = append(domains, expandEgressDomains(d, envVars)...)
		}
	}
	domains = append(domains, security.LLMProviderDomains(r.cfg.Config)...)
	domains = append(domains, security.LLMProviderEnvDomains(envVars)...)

	egressCfg, err := security.Resolve(
		r.cfg.Config.Egress.Profile,
		r.cfg.Config.Egress.Mode,
		domains,
		nil,
		r.cfg.Config.Egress.Capabilities,
	)
	if err != nil {
		r.logger.Warn("egress resolve failed; using unenforced client", map[string]any{"error": err.Error()})
		return http.DefaultClient, "", noop
	}

	allowPrivateIPs := false
	if r.cfg.Config.Egress.AllowPrivateIPs != nil {
		allowPrivateIPs = *r.cfg.Config.Egress.AllowPrivateIPs
	} else if security.InContainer() {
		allowPrivateIPs = true
	}

	enforcer := security.NewEgressEnforcer(nil, egressCfg.Mode, egressCfg.AllDomains, allowPrivateIPs)
	enforcer.OnAttempt = func(ctx context.Context, domain string, allowed bool) {
		audit.EmitFromContext(ctx, coreruntime.AuditEvent{
			Event:         egressEvent(allowed),
			CorrelationID: coreruntime.CorrelationIDFromContext(ctx),
			TaskID:        coreruntime.TaskIDFromContext(ctx),
			Fields:        map[string]any{"domain": domain, "mode": string(egressCfg.Mode)},
		})
	}
	egressClient := &http.Client{Transport: observability.WrapHTTPTransport(enforcer)}

	// Subprocess proxy for skill scripts (e.g. the weather skill's curl).
	if security.InContainer() || egressCfg.Mode == security.ModeDevOpen {
		return egressClient, "", noop
	}
	matcher := security.NewDomainMatcher(egressCfg.Mode, egressCfg.AllDomains)
	proxy := security.NewEgressProxy(matcher, allowPrivateIPs)
	proxy.OnAttempt = func(a security.EgressAttempt) {
		audit.Emit(coreruntime.AuditEvent{
			Event:         egressEvent(a.Allowed),
			TaskID:        a.TaskID,
			CorrelationID: a.CorrelationID,
			Fields:        map[string]any{"domain": a.Domain, "mode": string(egressCfg.Mode), "source": "proxy"},
		})
	}
	proxyURL, perr := proxy.Start(ctx)
	if perr != nil {
		r.logger.Warn("egress proxy failed to start; skills run unproxied", map[string]any{"error": perr.Error()})
		return egressClient, "", noop
	}
	return egressClient, proxyURL, func() { _ = proxy.Stop() }
}

func egressEvent(allowed bool) string {
	if allowed {
		return coreruntime.AuditEgressAllowed
	}
	return coreruntime.AuditEgressBlocked
}

// messageText concatenates the text parts of an a2a message.
func messageText(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range m.Parts {
		if p.Kind == a2a.PartKindText {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// osEnvMap snapshots the process environment as a map.
func osEnvMap() map[string]string {
	env := os.Environ()
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}
