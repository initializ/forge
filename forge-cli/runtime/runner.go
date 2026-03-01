package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/initializ/forge/forge-cli/server"
	cliskills "github.com/initializ/forge/forge-cli/skills"
	clitools "github.com/initializ/forge/forge-cli/tools"
	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/agentspec"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/llm/oauth"
	"github.com/initializ/forge/forge-core/llm/providers"
	"github.com/initializ/forge/forge-core/memory"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/secrets"
	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/tools"
	"github.com/initializ/forge/forge-core/tools/builtins"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/requirements"
	"github.com/initializ/forge/forge-skills/resolver"
)

// RunnerConfig holds configuration for the Runner.
type RunnerConfig struct {
	Config            *types.ForgeConfig
	WorkDir           string
	Port              int
	MockTools         bool
	EnforceGuardrails bool
	ModelOverride     string
	ProviderOverride  string
	EnvFilePath       string
	Verbose           bool
	Channels          []string // active channel adapters from --with flag
}

// Runner orchestrates the local A2A development server.
type Runner struct {
	cfg              RunnerConfig
	logger           coreruntime.Logger
	cliExecTool      *clitools.CLIExecuteTool
	modelConfig      *coreruntime.ModelConfig   // resolved model config (for banner)
	derivedCLIConfig *contract.DerivedCLIConfig // auto-derived from skill requirements
}

// NewRunner creates a Runner from the given config.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.Port <= 0 {
		cfg.Port = 8080
	}
	logger := coreruntime.NewJSONLogger(os.Stderr, cfg.Verbose)
	return &Runner{cfg: cfg, logger: logger}, nil
}

// Run starts the development server. It blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	// 0. Verify build output integrity if checksums.json exists.
	outputDir := filepath.Join(r.cfg.WorkDir, ".forge-output")
	if err := VerifyBuildOutput(outputDir); err != nil {
		r.logger.Warn("build output verification failed", map[string]any{"error": err.Error()})
	}

	// 1. Load .env file
	envVars, err := LoadEnvFile(r.cfg.EnvFilePath)
	if err != nil {
		return fmt.Errorf("loading env file: %w", err)
	}

	// Overlay secrets from configured providers
	r.overlaySecrets(envVars)

	// Apply model override
	if r.cfg.ModelOverride != "" {
		envVars["MODEL_NAME"] = r.cfg.ModelOverride
	}

	// 1b. Validate skill requirements
	if err := r.validateSkillRequirements(envVars); err != nil {
		return err
	}

	// 2. Load policy scaffold
	scaffold, err := LoadPolicyScaffold(r.cfg.WorkDir)
	if err != nil {
		r.logger.Warn("failed to load policy scaffold", map[string]any{"error": err.Error()})
	}
	guardrails := coreruntime.NewGuardrailEngine(scaffold, r.cfg.EnforceGuardrails, r.logger)

	// 3. Build agent card
	card, err := BuildAgentCard(r.cfg.WorkDir, r.cfg.Config, r.cfg.Port)
	if err != nil {
		return fmt.Errorf("building agent card: %w", err)
	}

	// 4. Create audit logger (used by hooks and handlers)
	auditLogger := coreruntime.NewAuditLogger(os.Stderr)

	// 4b. Resolve egress config and start proxy (if not in container)
	var egressClient *http.Client
	var egressProxy *security.EgressProxy
	var proxyURL string
	egressToolNames := make([]string, len(r.cfg.Config.Tools))
	for i, t := range r.cfg.Config.Tools {
		egressToolNames[i] = t.Name
	}
	// Merge skill-derived egress domains with explicitly configured domains.
	// Both sources may contain $VAR or ${VAR} references which are
	// expanded from .env and OS environment (e.g. "$K8S_API_DOMAIN").
	var egressDomains []string
	for _, d := range r.cfg.Config.Egress.AllowedDomains {
		egressDomains = append(egressDomains, expandEgressDomains(d, envVars)...)
	}
	if r.derivedCLIConfig != nil && len(r.derivedCLIConfig.EgressDomains) > 0 {
		for _, d := range r.derivedCLIConfig.EgressDomains {
			egressDomains = append(egressDomains, expandEgressDomains(d, envVars)...)
		}
	}
	egressCfg, egressErr := security.Resolve(
		r.cfg.Config.Egress.Profile,
		r.cfg.Config.Egress.Mode,
		egressDomains,
		egressToolNames,
		r.cfg.Config.Egress.Capabilities,
	)
	if egressErr != nil {
		r.logger.Warn("failed to resolve egress config, using default", map[string]any{"error": egressErr.Error()})
		egressClient = http.DefaultClient
	} else {
		enforcer := security.NewEgressEnforcer(nil, egressCfg.Mode, egressCfg.AllDomains)
		enforcer.OnAttempt = func(ctx context.Context, domain string, allowed bool) {
			event := coreruntime.AuditEgressAllowed
			if !allowed {
				event = coreruntime.AuditEgressBlocked
			}
			auditLogger.Emit(coreruntime.AuditEvent{
				Event:         event,
				CorrelationID: coreruntime.CorrelationIDFromContext(ctx),
				TaskID:        coreruntime.TaskIDFromContext(ctx),
				Fields:        map[string]any{"domain": domain, "mode": string(egressCfg.Mode)},
			})
		}
		egressClient = &http.Client{Transport: enforcer}

		// Start local proxy for subprocess egress enforcement
		if !security.InContainer() && egressCfg.Mode != security.ModeDevOpen {
			matcher := security.NewDomainMatcher(egressCfg.Mode, egressCfg.AllDomains)
			egressProxy = security.NewEgressProxy(matcher)
			egressProxy.OnAttempt = func(domain string, allowed bool) {
				event := coreruntime.AuditEgressAllowed
				if !allowed {
					event = coreruntime.AuditEgressBlocked
				}
				auditLogger.Emit(coreruntime.AuditEvent{
					Event:  event,
					Fields: map[string]any{"domain": domain, "mode": string(egressCfg.Mode), "source": "proxy"},
				})
			}
			var pErr error
			proxyURL, pErr = egressProxy.Start(ctx)
			if pErr != nil {
				r.logger.Warn("failed to start egress proxy", map[string]any{"error": pErr.Error()})
				egressProxy = nil
			} else {
				r.logger.Info("egress proxy started", map[string]any{"url": proxyURL})
			}
		}
	}
	if egressProxy != nil {
		defer egressProxy.Stop() //nolint:errcheck
	}

	// 5. Choose executor and optional lifecycle runtime
	var executor coreruntime.AgentExecutor
	var lifecycle coreruntime.AgentRuntime // optional, for subprocess lifecycle management
	if r.cfg.MockTools {
		toolSpecs := r.loadToolSpecs()
		executor = NewMockExecutor(toolSpecs)
		r.logger.Info("using mock executor", map[string]any{"tools": len(toolSpecs)})
	} else {
		switch r.cfg.Config.Framework {
		case "crewai", "langchain":
			rt := NewSubprocessRuntime(r.cfg.Config.Entrypoint, r.cfg.WorkDir, envVars, r.logger)
			lifecycle = rt
			executor = NewSubprocessExecutor(rt)
		default:
			// Forge framework — build tool registry and use built-in LLM executor
			reg := tools.NewRegistry()
			if err := builtins.RegisterAll(reg); err != nil {
				r.logger.Warn("failed to register builtin tools", map[string]any{"error": err.Error()})
			}

			// Register read_skill tool for lazy-loading skill instructions
			readSkill := builtins.NewReadSkillTool(r.cfg.WorkDir)
			if regErr := reg.Register(readSkill); regErr != nil {
				r.logger.Warn("failed to register read_skill", map[string]any{"error": regErr.Error()})
			}

			// Register cli_execute if configured explicitly or auto-derived from skills
			hasExplicitCLI := false
			for _, toolRef := range r.cfg.Config.Tools {
				if toolRef.Name == "cli_execute" && toolRef.Config != nil {
					hasExplicitCLI = true
					cliCfg := clitools.ParseCLIExecuteConfig(toolRef.Config)
					// Apply timeout hint from skill requirements if larger than explicit config
					if r.derivedCLIConfig != nil && r.derivedCLIConfig.TimeoutHint > cliCfg.TimeoutSeconds {
						cliCfg.TimeoutSeconds = r.derivedCLIConfig.TimeoutHint
					}
					if len(cliCfg.AllowedBinaries) > 0 {
						r.cliExecTool = clitools.NewCLIExecuteTool(cliCfg)
						if regErr := reg.Register(r.cliExecTool); regErr != nil {
							r.logger.Warn("failed to register cli_execute", map[string]any{"error": regErr.Error()})
						} else {
							avail, missing := r.cliExecTool.Availability()
							r.logger.Info("cli_execute registered", map[string]any{
								"available": len(avail), "missing": len(missing),
							})
						}
					}
					break
				}
			}
			// Auto-register cli_execute from skill-derived config when not explicitly configured
			if !hasExplicitCLI && r.derivedCLIConfig != nil && len(r.derivedCLIConfig.AllowedBinaries) > 0 {
				cliCfg := clitools.CLIExecuteConfig{
					AllowedBinaries: r.derivedCLIConfig.AllowedBinaries,
					EnvPassthrough:  r.derivedCLIConfig.EnvPassthrough,
					TimeoutSeconds:  r.derivedCLIConfig.TimeoutHint,
				}
				r.cliExecTool = clitools.NewCLIExecuteTool(cliCfg)
				if regErr := reg.Register(r.cliExecTool); regErr != nil {
					r.logger.Warn("failed to register auto-derived cli_execute", map[string]any{"error": regErr.Error()})
				} else {
					avail, missing := r.cliExecTool.Availability()
					r.logger.Info("cli_execute auto-registered from skill requirements", map[string]any{
						"binaries":  r.derivedCLIConfig.AllowedBinaries,
						"available": len(avail), "missing": len(missing),
					})
				}
			}

			// Discover custom tools in tools/ directory
			toolsDir := filepath.Join(r.cfg.WorkDir, "tools")
			discovered := clitools.DiscoverTools(toolsDir)
			cmdExec := &clitools.OSCommandExecutor{}
			for _, dt := range discovered {
				// Entrypoint must be relative to WorkDir so execution from agent root finds the file
				dtCopy := dt
				dtCopy.Entrypoint = filepath.Join("tools", dt.Entrypoint)
				ct := tools.NewCustomTool(dtCopy, cmdExec)
				if regErr := reg.Register(ct); regErr != nil {
					r.logger.Warn("failed to register custom tool", map[string]any{
						"tool": dt.Name, "error": regErr.Error(),
					})
				}
			}
			if len(discovered) > 0 {
				r.logger.Info("discovered custom tools", map[string]any{"count": len(discovered)})
			}

			// Set proxy URL on cli_execute tool
			if r.cliExecTool != nil && proxyURL != "" {
				r.cliExecTool.SetProxyURL(proxyURL)
			}

			// Register skill tools from skill files
			r.registerSkillTools(reg, proxyURL)

			// Remove denied tools from the registry, but preserve user-selected builtins
			if r.derivedCLIConfig != nil && len(r.derivedCLIConfig.DeniedTools) > 0 {
				userSelected := make(map[string]bool, len(r.cfg.Config.BuiltinTools))
				for _, name := range r.cfg.Config.BuiltinTools {
					userSelected[name] = true
				}

				var removed []string
				for _, denied := range r.derivedCLIConfig.DeniedTools {
					if userSelected[denied] {
						continue // user explicitly selected this tool, keep it
					}
					reg.Remove(denied)
					removed = append(removed, denied)
				}
				if len(removed) > 0 {
					r.logger.Info("removed denied tools", map[string]any{"denied": removed})
				}
			}

			// Log registered tool names
			toolNames := reg.List()
			r.logger.Info("registered tools", map[string]any{"tools": toolNames})

			// Try LLM executor, fall back to stub
			mc := coreruntime.ResolveModelConfig(r.cfg.Config, envVars, r.cfg.ProviderOverride)
			if mc != nil {
				r.modelConfig = mc
				llmClient, llmErr := r.buildLLMClient(mc)
				if llmErr != nil {
					r.logger.Warn("failed to create LLM client, using stub", map[string]any{"error": llmErr.Error()})
					executor = NewStubExecutor(r.cfg.Config.Framework)
				} else {
					// Build logging and audit hooks for agent loop observability
					hooks := coreruntime.NewHookRegistry()
					r.registerLoggingHooks(hooks)
					r.registerAuditHooks(hooks, auditLogger)
					r.registerProgressHooks(hooks)

					// Compute model-aware character budget.
					charBudget := r.cfg.Config.Memory.CharBudget
					if charBudget == 0 {
						charBudget = coreruntime.ContextBudgetForModel(mc.Client.Model)
					}

					execCfg := coreruntime.LLMExecutorConfig{
						Client:       llmClient,
						Tools:        reg,
						Hooks:        hooks,
						SystemPrompt: r.buildSystemPrompt(),
						Logger:       r.logger,
						ModelName:    mc.Client.Model,
						CharBudget:   charBudget,
					}

					// Initialize memory persistence (enabled by default).
					// Disable via FORGE_MEMORY_PERSISTENCE=false or memory.persistence: false in forge.yaml.
					memPersistence := true
					if r.cfg.Config.Memory.Persistence != nil {
						memPersistence = *r.cfg.Config.Memory.Persistence
					}
					if os.Getenv("FORGE_MEMORY_PERSISTENCE") == "false" {
						memPersistence = false
					}
					if memPersistence {
						sessDir := r.cfg.Config.Memory.SessionsDir
						if sessDir == "" {
							sessDir = filepath.Join(r.cfg.WorkDir, ".forge", "sessions")
						}
						memStore, storeErr := coreruntime.NewMemoryStore(sessDir)
						if storeErr != nil {
							r.logger.Warn("failed to create memory store, persistence disabled", map[string]any{
								"error": storeErr.Error(),
							})
						} else {
							// Clean up old sessions on startup (7-day TTL).
							deleted, _ := memStore.Cleanup(7 * 24 * time.Hour)
							if deleted > 0 {
								r.logger.Info("cleaned up old sessions", map[string]any{"deleted": deleted})
							}

							compactor := coreruntime.NewCompactor(coreruntime.CompactorConfig{
								Client:       llmClient,
								Store:        memStore,
								Logger:       r.logger,
								CharBudget:   charBudget,
								TriggerRatio: r.cfg.Config.Memory.TriggerRatio,
							})

							execCfg.Store = memStore
							execCfg.Compactor = compactor
							r.logger.Info("memory persistence enabled", map[string]any{
								"sessions_dir": sessDir,
							})
						}
					}

					// Initialize long-term memory if enabled.
					memMgr := r.initLongTermMemory(ctx, mc, reg, execCfg.Compactor)
					if memMgr != nil {
						defer memMgr.Close() //nolint:errcheck
					}

					executor = coreruntime.NewLLMExecutor(execCfg)

					r.logger.Info("using LLM executor", map[string]any{
						"provider":  mc.Provider,
						"model":     mc.Client.Model,
						"tools":     len(toolNames),
						"fallbacks": len(mc.Fallbacks),
					})
				}
			} else {
				executor = NewStubExecutor(r.cfg.Config.Framework)
				r.logger.Warn("no LLM provider configured, using stub executor", map[string]any{
					"framework": r.cfg.Config.Framework,
				})
			}
		}
	}
	defer executor.Close() //nolint:errcheck

	// Start lifecycle runtime if present
	if lifecycle != nil {
		if err := lifecycle.Start(ctx); err != nil {
			return fmt.Errorf("starting runtime: %w", err)
		}
		defer lifecycle.Stop() //nolint:errcheck
	}

	// 6. Create A2A server
	srv := server.NewServer(server.ServerConfig{
		Port:      r.cfg.Port,
		AgentCard: card,
	})

	// 7. Register JSON-RPC handlers
	r.registerHandlers(srv, executor, guardrails, egressClient, auditLogger)

	// 9. Start file watcher
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	watcher := NewFileWatcher(r.cfg.WorkDir, func() {
		// Reload config and agent card
		newCard, err := BuildAgentCard(r.cfg.WorkDir, r.cfg.Config, r.cfg.Port)
		if err != nil {
			r.logger.Error("failed to reload agent card", map[string]any{"error": err.Error()})
		} else {
			srv.UpdateAgentCard(newCard)
			r.logger.Info("agent card reloaded", nil)
		}

		// Restart subprocess lifecycle (no-op if lifecycle is nil)
		if lifecycle != nil {
			if err := lifecycle.Restart(ctx); err != nil {
				r.logger.Error("failed to restart runtime", map[string]any{"error": err.Error()})
			}
		}
	}, r.logger)
	go watcher.Watch(watchCtx)

	// 10. Print startup banner
	r.printBanner(proxyURL)

	// 11. Start server (blocks)
	return srv.Start(ctx)
}

func (r *Runner) registerHandlers(srv *server.Server, executor coreruntime.AgentExecutor, guardrails *coreruntime.GuardrailEngine, egressClient *http.Client, auditLogger *coreruntime.AuditLogger) {
	store := srv.TaskStore()

	// tasks/send — synchronous request
	srv.RegisterHandler("tasks/send", func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse {
		var params a2a.SendTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid params: "+err.Error())
		}

		r.logger.Info("tasks/send", map[string]any{"task_id": params.ID})

		// Inject egress client and correlation/task IDs into context
		correlationID := coreruntime.GenerateID()
		ctx = security.WithEgressClient(ctx, egressClient)
		ctx = coreruntime.WithCorrelationID(ctx, correlationID)
		ctx = coreruntime.WithTaskID(ctx, params.ID)

		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionStart,
			CorrelationID: correlationID,
			TaskID:        params.ID,
		})

		// Load existing task to preserve conversation history, or create new.
		task := store.Get(params.ID)
		if task == nil {
			task = &a2a.Task{ID: params.ID}
		}
		task.Status = a2a.TaskStatus{State: a2a.TaskStateSubmitted}
		store.Put(task)

		// Guardrail check inbound
		if err := guardrails.CheckInbound(&params.Message); err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("Guardrail violation: " + err.Error())},
				},
			}
			store.Put(task)
			auditLogger.Emit(coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return a2a.NewResponse(id, task)
		}

		// Append inbound user message to task history.
		task.History = append(task.History, params.Message)

		// Update to working
		task.Status = a2a.TaskStatus{State: a2a.TaskStateWorking}
		store.Put(task)

		// Execute via executor
		respMsg, err := executor.Execute(ctx, task, &params.Message)
		if err != nil {
			r.logger.Error("execute failed", map[string]any{"task_id": params.ID, "error": err.Error()})
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart(err.Error())},
				},
			}
			store.Put(task)
			auditLogger.Emit(coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return a2a.NewResponse(id, task)
		}

		// Guardrail check outbound
		if respMsg != nil {
			if err := guardrails.CheckOutbound(respMsg); err != nil {
				task.Status = a2a.TaskStatus{
					State: a2a.TaskStateFailed,
					Message: &a2a.Message{
						Role:  a2a.MessageRoleAgent,
						Parts: []a2a.Part{a2a.NewTextPart("Outbound guardrail violation: " + err.Error())},
					},
				}
				store.Put(task)
				auditLogger.Emit(coreruntime.AuditEvent{
					Event:         coreruntime.AuditSessionEnd,
					CorrelationID: correlationID,
					TaskID:        params.ID,
					Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
				})
				return a2a.NewResponse(id, task)
			}
		}

		// Append agent response to task history.
		if respMsg != nil {
			task.History = append(task.History, *respMsg)
		}

		// Build completed task
		task.Status = a2a.TaskStatus{
			State:   a2a.TaskStateCompleted,
			Message: respMsg,
		}
		if respMsg != nil {
			task.Artifacts = []a2a.Artifact{
				{
					Name:  "response",
					Parts: respMsg.Parts,
				},
			}
		}
		store.Put(task)
		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionEnd,
			CorrelationID: correlationID,
			TaskID:        params.ID,
			Fields:        map[string]any{"state": string(task.Status.State)},
		})
		r.logger.Info("task completed", map[string]any{"task_id": params.ID, "state": string(task.Status.State)})
		return a2a.NewResponse(id, task)
	})

	// tasks/sendSubscribe — SSE streaming
	srv.RegisterSSEHandler("tasks/sendSubscribe", func(ctx context.Context, id any, rawParams json.RawMessage, w http.ResponseWriter, flusher http.Flusher) {
		var params a2a.SendTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			server.WriteSSEEvent(w, flusher, "error", a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, err.Error())) //nolint:errcheck
			return
		}

		r.logger.Info("tasks/sendSubscribe", map[string]any{"task_id": params.ID})

		// Inject egress client and correlation/task IDs into context
		correlationID := coreruntime.GenerateID()
		ctx = security.WithEgressClient(ctx, egressClient)
		ctx = coreruntime.WithCorrelationID(ctx, correlationID)
		ctx = coreruntime.WithTaskID(ctx, params.ID)

		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionStart,
			CorrelationID: correlationID,
			TaskID:        params.ID,
		})

		// Load existing task to preserve conversation history, or create new.
		task := store.Get(params.ID)
		if task == nil {
			task = &a2a.Task{ID: params.ID}
		}
		task.Status = a2a.TaskStatus{State: a2a.TaskStateSubmitted}
		store.Put(task)
		server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck

		// Guardrail check inbound
		if err := guardrails.CheckInbound(&params.Message); err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart("Guardrail violation: " + err.Error())},
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck
			auditLogger.Emit(coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return
		}

		// Append inbound user message to task history.
		task.History = append(task.History, params.Message)

		// Update to working
		task.Status = a2a.TaskStatus{State: a2a.TaskStateWorking}
		store.Put(task)
		server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck

		// Inject progress emitter for SSE clients
		ctx = coreruntime.WithProgressEmitter(ctx, func(event coreruntime.ProgressEvent) {
			progressTask := &a2a.Task{
				ID: params.ID,
				Status: a2a.TaskStatus{
					State: a2a.TaskStateWorking,
					Message: &a2a.Message{
						Role:  a2a.MessageRoleAgent,
						Parts: []a2a.Part{a2a.NewTextPart(event.Message)},
					},
				},
				Metadata: map[string]any{
					"progress_phase": event.Phase,
					"progress_tool":  event.Tool,
				},
			}
			server.WriteSSEEvent(w, flusher, "progress", progressTask) //nolint:errcheck
		})

		// Stream from executor
		ch, err := executor.ExecuteStream(ctx, task, &params.Message)
		if err != nil {
			task.Status = a2a.TaskStatus{
				State: a2a.TaskStateFailed,
				Message: &a2a.Message{
					Role:  a2a.MessageRoleAgent,
					Parts: []a2a.Part{a2a.NewTextPart(err.Error())},
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "status", task) //nolint:errcheck
			auditLogger.Emit(coreruntime.AuditEvent{
				Event:         coreruntime.AuditSessionEnd,
				CorrelationID: correlationID,
				TaskID:        params.ID,
				Fields:        map[string]any{"state": string(a2a.TaskStateFailed)},
			})
			return
		}

		var finalState a2a.TaskState
		for respMsg := range ch {
			// Guardrail check outbound
			if grErr := guardrails.CheckOutbound(respMsg); grErr != nil {
				task.Status = a2a.TaskStatus{
					State: a2a.TaskStateFailed,
					Message: &a2a.Message{
						Role:  a2a.MessageRoleAgent,
						Parts: []a2a.Part{a2a.NewTextPart("Outbound guardrail violation: " + grErr.Error())},
					},
				}
				store.Put(task)
				server.WriteSSEEvent(w, flusher, "result", task) //nolint:errcheck
				finalState = a2a.TaskStateFailed
				break
			}

			// Append agent response to task history.
			task.History = append(task.History, *respMsg)

			// Build completed result
			task.Status = a2a.TaskStatus{
				State:   a2a.TaskStateCompleted,
				Message: respMsg,
			}
			task.Artifacts = []a2a.Artifact{
				{
					Name:  "response",
					Parts: respMsg.Parts,
				},
			}
			store.Put(task)
			server.WriteSSEEvent(w, flusher, "result", task) //nolint:errcheck
			finalState = a2a.TaskStateCompleted
		}

		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditSessionEnd,
			CorrelationID: correlationID,
			TaskID:        params.ID,
			Fields:        map[string]any{"state": string(finalState)},
		})
	})

	// tasks/get — lookup task by ID
	srv.RegisterHandler("tasks/get", func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse {
		var params a2a.GetTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid params: "+err.Error())
		}

		task := store.Get(params.ID)
		if task == nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "task not found: "+params.ID)
		}
		return a2a.NewResponse(id, task)
	})

	// tasks/cancel — cancel a task
	srv.RegisterHandler("tasks/cancel", func(ctx context.Context, id any, rawParams json.RawMessage) *a2a.JSONRPCResponse {
		var params a2a.CancelTaskParams
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "invalid params: "+err.Error())
		}

		task := store.Get(params.ID)
		if task == nil {
			return a2a.NewErrorResponse(id, a2a.ErrCodeInvalidParams, "task not found: "+params.ID)
		}

		task.Status = a2a.TaskStatus{State: a2a.TaskStateCanceled}
		store.Put(task)
		r.logger.Info("task canceled", map[string]any{"task_id": params.ID})
		return a2a.NewResponse(id, task)
	})
}

func (r *Runner) loadToolSpecs() []agentspec.ToolSpec {
	var toolSpecs []agentspec.ToolSpec
	for _, t := range r.cfg.Config.Tools {
		toolSpecs = append(toolSpecs, agentspec.ToolSpec{Name: t.Name})
	}
	return toolSpecs
}

// registerLoggingHooks adds observability hooks to the LLM executor's agent loop.
func (r *Runner) registerLoggingHooks(hooks *coreruntime.HookRegistry) {
	hooks.Register(coreruntime.AfterLLMCall, func(_ context.Context, hctx *coreruntime.HookContext) error {
		if hctx.Response == nil {
			return nil
		}
		fields := map[string]any{
			"finish_reason": hctx.Response.FinishReason,
		}
		if hctx.Response.Usage.TotalTokens > 0 {
			fields["tokens"] = hctx.Response.Usage.TotalTokens
		}
		if len(hctx.Response.Message.ToolCalls) > 0 {
			names := make([]string, len(hctx.Response.Message.ToolCalls))
			for i, tc := range hctx.Response.Message.ToolCalls {
				names[i] = tc.Function.Name
			}
			fields["tool_calls"] = names
		}
		if hctx.Response.Message.Content != "" {
			content := hctx.Response.Message.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fields["response"] = content
		}
		r.logger.Info("llm response", fields)
		return nil
	})

	hooks.Register(coreruntime.BeforeToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{"tool": hctx.ToolName}
		if hctx.ToolInput != "" {
			input := hctx.ToolInput
			if len(input) > 300 {
				input = input[:300] + "..."
			}
			fields["input"] = input
		}
		r.logger.Info("tool call", fields)
		return nil
	})

	hooks.Register(coreruntime.AfterToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{"tool": hctx.ToolName}
		if hctx.Error != nil {
			fields["error"] = hctx.Error.Error()
			r.logger.Error("tool error", fields)
		} else {
			output := hctx.ToolOutput
			if len(output) > 500 {
				output = output[:500] + "..."
			}
			fields["output_length"] = len(hctx.ToolOutput)
			fields["output"] = output
			r.logger.Info("tool result", fields)
		}
		return nil
	})

	hooks.Register(coreruntime.OnError, func(_ context.Context, hctx *coreruntime.HookContext) error {
		if hctx.Error != nil {
			r.logger.Error("agent loop error", map[string]any{"error": hctx.Error.Error()})
		}
		return nil
	})
}

// registerAuditHooks adds structured audit event hooks to the LLM executor's agent loop.
func (r *Runner) registerAuditHooks(hooks *coreruntime.HookRegistry, auditLogger *coreruntime.AuditLogger) {
	hooks.Register(coreruntime.BeforeToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditToolExec,
			CorrelationID: hctx.CorrelationID,
			TaskID:        hctx.TaskID,
			Fields:        map[string]any{"tool": hctx.ToolName, "phase": "start"},
		})
		return nil
	})

	hooks.Register(coreruntime.AfterToolExec, func(_ context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{"tool": hctx.ToolName, "phase": "end"}
		if hctx.Error != nil {
			fields["error"] = hctx.Error.Error()
		}
		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditToolExec,
			CorrelationID: hctx.CorrelationID,
			TaskID:        hctx.TaskID,
			Fields:        fields,
		})
		return nil
	})

	hooks.Register(coreruntime.AfterLLMCall, func(_ context.Context, hctx *coreruntime.HookContext) error {
		fields := map[string]any{}
		if hctx.Response != nil && hctx.Response.Usage.TotalTokens > 0 {
			fields["tokens"] = hctx.Response.Usage.TotalTokens
		}
		auditLogger.Emit(coreruntime.AuditEvent{
			Event:         coreruntime.AuditLLMCall,
			CorrelationID: hctx.CorrelationID,
			TaskID:        hctx.TaskID,
			Fields:        fields,
		})
		return nil
	})
}

// registerProgressHooks adds hooks that emit progress events via ProgressEmitter.
// The emitter is injected into context by SSE handlers so clients receive real-time
// progress during long-running tool executions.
func (r *Runner) registerProgressHooks(hooks *coreruntime.HookRegistry) {
	hooks.Register(coreruntime.BeforeToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		if emitter := coreruntime.ProgressEmitterFromContext(ctx); emitter != nil {
			emitter(coreruntime.ProgressEvent{
				Phase:   "tool_start",
				Tool:    hctx.ToolName,
				Message: fmt.Sprintf("Executing %s...", hctx.ToolName),
			})
		}
		return nil
	})

	hooks.Register(coreruntime.AfterToolExec, func(ctx context.Context, hctx *coreruntime.HookContext) error {
		if emitter := coreruntime.ProgressEmitterFromContext(ctx); emitter != nil {
			msg := fmt.Sprintf("Completed %s", hctx.ToolName)
			if hctx.Error != nil {
				msg = fmt.Sprintf("Failed %s: %s", hctx.ToolName, hctx.Error.Error())
			}
			emitter(coreruntime.ProgressEvent{
				Phase:   "tool_end",
				Tool:    hctx.ToolName,
				Message: msg,
			})
		}
		return nil
	})
}

// buildLLMClient creates the LLM client from the resolved model config.
// If fallback providers are configured, wraps them in a FallbackChain.
func (r *Runner) buildLLMClient(mc *coreruntime.ModelConfig) (llm.Client, error) {
	primaryClient, err := r.createProviderClient(mc.Provider, mc.Client)
	if err != nil {
		return nil, err
	}

	// No fallbacks — return primary client directly
	if len(mc.Fallbacks) == 0 {
		return primaryClient, nil
	}

	// Build fallback chain
	candidates := []llm.FallbackCandidate{
		{Provider: mc.Provider, Model: mc.Client.Model, Client: primaryClient},
	}
	for _, fb := range mc.Fallbacks {
		fbClient, fbErr := r.createProviderClient(fb.Provider, fb.Client)
		if fbErr != nil {
			r.logger.Warn("skipping fallback provider", map[string]any{
				"provider": fb.Provider, "error": fbErr.Error(),
			})
			continue
		}
		candidates = append(candidates, llm.FallbackCandidate{
			Provider: fb.Provider,
			Model:    fb.Client.Model,
			Client:   fbClient,
		})
	}

	return llm.NewFallbackChain(candidates), nil
}

// createProviderClient creates an LLM client for a provider, using OAuth
// credentials if available for supported providers.
func (r *Runner) createProviderClient(provider string, cfg llm.ClientConfig) (llm.Client, error) {
	// Check for stored OAuth credentials — but only if no real API key is
	// configured. The "__oauth__" sentinel means the user chose OAuth auth
	// during init, so we should load the actual token from the credential store.
	if provider == "openai" && (cfg.APIKey == "" || cfg.APIKey == "__oauth__") {
		token, err := oauth.LoadCredentials(provider)
		if err == nil && token != nil && token.RefreshToken != "" {
			oauthCfg := oauth.OpenAIConfig()
			// Use token's base URL, or fall back to the OAuth config default
			baseURL := token.BaseURL
			if baseURL == "" {
				baseURL = oauthCfg.BaseURL
			}
			r.logger.Info("using OAuth credentials for provider", map[string]any{
				"provider": provider,
				"base_url": baseURL,
			})
			cfg.APIKey = token.AccessToken
			cfg.BaseURL = baseURL
			return providers.NewOAuthClient(cfg, provider, oauthCfg), nil
		}
	}

	return providers.NewClient(provider, cfg)
}

func (r *Runner) printBanner(proxyURL string) {
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Forge Dev Server\n")
	fmt.Fprintf(os.Stderr, "  ────────────────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Agent:      %s (v%s)\n", r.cfg.Config.AgentID, r.cfg.Config.Version)
	fmt.Fprintf(os.Stderr, "  Framework:  %s\n", r.cfg.Config.Framework)
	fmt.Fprintf(os.Stderr, "  Port:       %d\n", r.cfg.Port)
	if r.cfg.MockTools {
		fmt.Fprintf(os.Stderr, "  Mode:       mock (no subprocess)\n")
	} else if r.cfg.Config.Entrypoint != "" {
		fmt.Fprintf(os.Stderr, "  Entrypoint: %s\n", r.cfg.Config.Entrypoint)
	}
	// Model info
	if r.modelConfig != nil {
		fmt.Fprintf(os.Stderr, "  Model:      %s/%s\n", r.modelConfig.Provider, r.modelConfig.Client.Model)
		if len(r.modelConfig.Fallbacks) > 0 {
			var fbNames []string
			for _, fb := range r.modelConfig.Fallbacks {
				fbNames = append(fbNames, fb.Provider+"/"+fb.Client.Model)
			}
			fmt.Fprintf(os.Stderr, "  Fallbacks:  %s\n", strings.Join(fbNames, ", "))
		}
	}
	// Tools
	if len(r.cfg.Config.Tools) > 0 {
		names := make([]string, 0, len(r.cfg.Config.Tools))
		for _, t := range r.cfg.Config.Tools {
			names = append(names, t.Name)
		}
		fmt.Fprintf(os.Stderr, "  Tools:      %d (%s)\n", len(names), strings.Join(names, ", "))
	}
	// CLI Exec binaries
	if r.cliExecTool != nil {
		avail, missing := r.cliExecTool.Availability()
		total := len(avail) + len(missing)
		parts := make([]string, 0, total)
		for _, b := range avail {
			parts = append(parts, b+" ok")
		}
		for _, b := range missing {
			parts = append(parts, b+" MISSING")
		}
		fmt.Fprintf(os.Stderr, "  CLI Exec:   %d/%d binaries (%s)\n", len(avail), total, strings.Join(parts, ", "))
	}
	// Channels
	if len(r.cfg.Channels) > 0 {
		fmt.Fprintf(os.Stderr, "  Channels:   %s\n", strings.Join(r.cfg.Channels, ", "))
	}
	// Egress
	if r.cfg.Config.Egress.Profile != "" || r.cfg.Config.Egress.Mode != "" {
		fmt.Fprintf(os.Stderr, "  Egress:     %s / %s\n",
			defaultStr(r.cfg.Config.Egress.Profile, "strict"),
			defaultStr(r.cfg.Config.Egress.Mode, "deny-all"))
	}
	// Egress proxy
	if proxyURL != "" {
		fmt.Fprintf(os.Stderr, "  Proxy:      %s\n", proxyURL)
	}
	fmt.Fprintf(os.Stderr, "  ────────────────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Agent Card: http://localhost:%d/.well-known/agent.json\n", r.cfg.Port)
	fmt.Fprintf(os.Stderr, "  Health:     http://localhost:%d/healthz\n", r.cfg.Port)
	fmt.Fprintf(os.Stderr, "  JSON-RPC:   POST http://localhost:%d/\n", r.cfg.Port)
	fmt.Fprintf(os.Stderr, "  ────────────────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Press Ctrl+C to stop\n\n")
}

// discoverSkillFiles returns all skill file paths from both flat and subdirectory formats,
// plus the main SKILL.md (or custom path from forge.yaml).
func (r *Runner) discoverSkillFiles() []string {
	skillsDir := filepath.Join(r.cfg.WorkDir, "skills")

	// Flat format: skills/*.md
	matches, _ := filepath.Glob(filepath.Join(skillsDir, "*.md"))

	// Subdirectory format: skills/*/SKILL.md
	subDirMatches, _ := filepath.Glob(filepath.Join(skillsDir, "*", "SKILL.md"))
	matches = append(matches, subDirMatches...)

	// Main SKILL.md (or custom path from forge.yaml)
	mainSkill := "SKILL.md"
	if r.cfg.Config.Skills.Path != "" {
		mainSkill = r.cfg.Config.Skills.Path
	}
	if !filepath.IsAbs(mainSkill) {
		mainSkill = filepath.Join(r.cfg.WorkDir, mainSkill)
	}
	if info, err := os.Stat(mainSkill); err == nil && !info.IsDir() {
		matches = append(matches, mainSkill)
	}

	return matches
}

// registerSkillTools scans skill files for skill entries that have associated
// scripts. Each script-backed skill is registered as a first-class tool in the registry.
func (r *Runner) registerSkillTools(reg *tools.Registry, proxyURL string) {
	matches := r.discoverSkillFiles()

	var registered int
	for _, match := range matches {
		entries, meta, err := cliskills.ParseFileWithMetadata(match)
		if err != nil {
			continue
		}

		// Derive skill directory name from the SKILL.md path (for subdirectory skills)
		skillDirName := ""
		if strings.HasSuffix(match, "/SKILL.md") {
			skillDirName = filepath.Base(filepath.Dir(match))
		}

		for _, entry := range entries {
			// Map tool name (underscores) to script name (hyphens)
			scriptName := strings.ReplaceAll(entry.Name, "_", "-")

			// Look for scripts in subdirectory layout first: skills/{dir}/scripts/{name}.sh
			// Then fall back to legacy flat layout: skills/scripts/{name}.sh
			var scriptPath string
			if skillDirName != "" {
				candidate := filepath.Join("skills", skillDirName, "scripts", scriptName+".sh")
				if _, err := os.Stat(filepath.Join(r.cfg.WorkDir, candidate)); err == nil {
					scriptPath = candidate
				}
			}
			if scriptPath == "" {
				candidate := filepath.Join("skills", "scripts", scriptName+".sh")
				if _, err := os.Stat(filepath.Join(r.cfg.WorkDir, candidate)); err == nil {
					scriptPath = candidate
				}
			}
			if scriptPath == "" {
				continue // No script file, skip
			}

			// Extract timeout_hint from metadata
			timeout := 120 * time.Second
			if meta != nil && meta.Metadata != nil {
				if forgeMap, ok := meta.Metadata["forge"]; ok {
					if raw, ok := forgeMap["timeout_hint"]; ok {
						switch v := raw.(type) {
						case int:
							timeout = time.Duration(v) * time.Second
						case float64:
							timeout = time.Duration(int(v)) * time.Second
						}
					}
				}
			}

			// Collect env vars for passthrough
			var envVars []string
			if entry.ForgeReqs != nil && entry.ForgeReqs.Env != nil {
				envVars = append(envVars, entry.ForgeReqs.Env.Required...)
				envVars = append(envVars, entry.ForgeReqs.Env.Optional...)
			}

			skillExec := &clitools.SkillCommandExecutor{
				Timeout:  timeout,
				EnvVars:  envVars,
				ProxyURL: proxyURL,
			}

			st := tools.NewSkillTool(entry.Name, entry.Description, entry.InputSpec, scriptPath, skillExec)
			if err := reg.Register(st); err != nil {
				r.logger.Warn("failed to register skill tool", map[string]any{
					"skill": entry.Name, "error": err.Error(),
				})
			} else {
				registered++
			}
		}
	}
	if registered > 0 {
		r.logger.Info("registered skill tools", map[string]any{"count": registered})
	}
}

// buildSystemPrompt constructs the system prompt with an optional skill catalog.
func (r *Runner) buildSystemPrompt() string {
	base := fmt.Sprintf("You are %s, an AI agent.", r.cfg.Config.AgentID)
	catalog := r.buildSkillCatalog()
	if catalog == "" {
		return base
	}
	return base + "\n\n" + catalog
}

// buildSkillCatalog generates a lightweight catalog of binary-backed skills
// (those without scripts) for the system prompt. Script-backed skills are
// already registered as first-class tools and don't need catalog entries.
func (r *Runner) buildSkillCatalog() string {
	matches := r.discoverSkillFiles()
	if len(matches) == 0 {
		return ""
	}

	var catalogEntries []string
	for _, match := range matches {
		entries, _, err := cliskills.ParseFileWithMetadata(match)
		if err != nil {
			continue
		}

		// Derive skill directory name from the SKILL.md path (for subdirectory skills)
		catalogSkillDir := ""
		if strings.HasSuffix(match, "/SKILL.md") {
			catalogSkillDir = filepath.Base(filepath.Dir(match))
		}

		for _, entry := range entries {
			// Skip skills that have scripts (already registered as tools)
			scriptName := strings.ReplaceAll(entry.Name, "_", "-")
			hasScript := false
			// Check subdirectory layout: skills/{dir}/scripts/{name}.sh
			if catalogSkillDir != "" {
				sp := filepath.Join(r.cfg.WorkDir, "skills", catalogSkillDir, "scripts", scriptName+".sh")
				if _, err := os.Stat(sp); err == nil {
					hasScript = true
				}
			}
			// Check legacy flat layout: skills/scripts/{name}.sh
			if !hasScript {
				sp := filepath.Join(r.cfg.WorkDir, "skills", "scripts", scriptName+".sh")
				if _, err := os.Stat(sp); err == nil {
					hasScript = true
				}
			}
			if hasScript {
				continue
			}

			if entry.Name != "" && entry.Description != "" {
				line := fmt.Sprintf("- %s: %s", entry.Name, entry.Description)
				// Add tool hint when skill requires specific binaries
				if entry.ForgeReqs != nil && len(entry.ForgeReqs.Bins) > 0 {
					line += fmt.Sprintf(" (use cli_execute with: %s)", strings.Join(entry.ForgeReqs.Bins, ", "))
				}
				catalogEntries = append(catalogEntries, line)

				// Include full skill instructions when available
				if entry.Body != "" {
					catalogEntries = append(catalogEntries, "")
					catalogEntries = append(catalogEntries, entry.Body)
					catalogEntries = append(catalogEntries, "")
				}
			}
		}
	}

	if len(catalogEntries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Available Skills\n\n")
	for _, entry := range catalogEntries {
		b.WriteString(entry)
		b.WriteString("\n")
	}
	return b.String()
}

// validateSkillRequirements loads skill requirements and validates them.
// It also auto-derives cli_execute config from skill requirements.
func (r *Runner) validateSkillRequirements(envVars map[string]string) error {
	matches := r.discoverSkillFiles()
	if len(matches) == 0 {
		return nil
	}

	var allEntries []contract.SkillEntry
	for _, match := range matches {
		entries, _, err := cliskills.ParseFileWithMetadata(match)
		if err != nil {
			r.logger.Warn("failed to parse skills with metadata", map[string]any{
				"file": match, "error": err.Error(),
			})
			continue
		}
		allEntries = append(allEntries, entries...)
	}
	if len(allEntries) == 0 {
		return nil
	}

	entries := allEntries

	reqs := requirements.AggregateRequirements(entries)
	if len(reqs.Bins) == 0 && len(reqs.EnvRequired) == 0 && len(reqs.EnvOneOf) == 0 && len(reqs.EnvOptional) == 0 {
		return nil
	}

	// Build env resolver
	osEnv := envFromOS()
	envResolver := resolver.NewEnvResolver(osEnv, envVars, nil)

	// Check binaries
	binDiags := resolver.BinDiagnostics(reqs.Bins)
	for _, d := range binDiags {
		r.logger.Warn(d.Message, nil)
	}

	// Check env vars
	envDiags := envResolver.Resolve(reqs)
	for _, d := range envDiags {
		switch d.Level {
		case "error":
			return fmt.Errorf("skill requirement not met: %s", d.Message)
		case "warning":
			r.logger.Warn(d.Message, nil)
		}
	}

	// Auto-derive cli_execute config from skill requirements
	derived := requirements.DeriveCLIConfig(reqs)
	if derived != nil && len(derived.AllowedBinaries) > 0 {
		// Check if cli_execute is already explicitly configured
		hasExplicit := false
		for _, toolRef := range r.cfg.Config.Tools {
			if toolRef.Name == "cli_execute" {
				hasExplicit = true
				break
			}
		}

		if !hasExplicit {
			fields := map[string]any{
				"binaries": len(derived.AllowedBinaries),
				"env_vars": len(derived.EnvPassthrough),
			}
			if derived.TimeoutHint > 0 {
				fields["timeout_hint"] = derived.TimeoutHint
			}
			r.logger.Info("auto-derived cli_execute from skill requirements", fields)
		}
	}

	// Store the derived config for use during executor setup
	r.derivedCLIConfig = derived

	return nil
}

func envFromOS() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if ok {
			env[k] = v
		}
	}
	return env
}

// expandEgressDomains expands $VAR and ${VAR} references in an egress domain
// string using the provided env vars map, falling back to OS environment.
// The expanded result is split on commas so a single env var can provide
// multiple domains (e.g. K8S_API_DOMAIN="a.eks.amazonaws.com,b.azmk8s.io").
// Returns nil if the domain is a pure variable reference that resolves to empty.
func expandEgressDomains(domain string, envVars map[string]string) []string {
	if !strings.Contains(domain, "$") {
		return []string{domain} // no variable reference, return as-is
	}

	result := os.Expand(domain, func(key string) string {
		if v, ok := envVars[key]; ok && v != "" {
			return v
		}
		return os.Getenv(key)
	})

	result = strings.TrimSpace(result)
	if result == "" {
		return nil
	}

	// Split on commas to support multiple domains in a single variable.
	parts := strings.Split(result, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// initLongTermMemory sets up the long-term memory system if enabled.
// It resolves the embedder, creates a memory.Manager, registers memory tools,
// and starts background indexing. Returns the Manager (caller must Close) or nil.
func (r *Runner) initLongTermMemory(ctx context.Context, mc *coreruntime.ModelConfig, reg *tools.Registry, compactor *coreruntime.Compactor) *memory.Manager {
	// Check if long-term memory is enabled.
	enabled := false
	if r.cfg.Config.Memory.LongTerm != nil {
		enabled = *r.cfg.Config.Memory.LongTerm
	}
	if os.Getenv("FORGE_MEMORY_LONG_TERM") == "true" {
		enabled = true
	}
	if !enabled {
		return nil
	}

	memDir := r.cfg.Config.Memory.MemoryDir
	if memDir == "" {
		memDir = filepath.Join(r.cfg.WorkDir, ".forge", "memory")
	}

	// Resolve embedder.
	embedder := r.resolveEmbedder(mc)

	// Build search config from forge.yaml.
	searchCfg := memory.DefaultSearchConfig()
	if r.cfg.Config.Memory.VectorWeight > 0 {
		searchCfg.VectorWeight = r.cfg.Config.Memory.VectorWeight
	}
	if r.cfg.Config.Memory.KeywordWeight > 0 {
		searchCfg.KeywordWeight = r.cfg.Config.Memory.KeywordWeight
	}
	if r.cfg.Config.Memory.DecayHalfLifeDays > 0 {
		searchCfg.DecayHalfLife = time.Duration(r.cfg.Config.Memory.DecayHalfLifeDays) * 24 * time.Hour
	}

	mgr, err := memory.NewManager(memory.ManagerConfig{
		MemoryDir:    memDir,
		Embedder:     embedder,
		Logger:       r.logger,
		SearchConfig: searchCfg,
	})
	if err != nil {
		r.logger.Warn("failed to create memory manager, long-term memory disabled", map[string]any{
			"error": err.Error(),
		})
		return nil
	}

	// Register memory tools.
	if regErr := reg.Register(builtins.NewMemorySearchTool(mgr)); regErr != nil {
		r.logger.Warn("failed to register memory_search tool", map[string]any{"error": regErr.Error()})
	}
	if regErr := reg.Register(builtins.NewMemoryGetTool(mgr)); regErr != nil {
		r.logger.Warn("failed to register memory_get tool", map[string]any{"error": regErr.Error()})
	}

	// Wire memory flusher into compactor (if compactor exists).
	if compactor != nil {
		compactor.SetMemoryFlusher(mgr)
	}

	// Index memory files at startup in background.
	go func() {
		if idxErr := mgr.IndexAll(ctx); idxErr != nil {
			r.logger.Warn("background memory indexing failed", map[string]any{"error": idxErr.Error()})
		}
	}()

	mode := "keyword-only"
	if embedder != nil {
		mode = "vector+keyword"
	}
	r.logger.Info("long-term memory enabled", map[string]any{
		"memory_dir": memDir,
		"mode":       mode,
	})

	return mgr
}

// resolveEmbedder creates an embedder from config or auto-detection.
// Returns nil if no embedder can be created (keyword-only mode).
func (r *Runner) resolveEmbedder(mc *coreruntime.ModelConfig) llm.Embedder {
	// Resolution order: config override → env → primary LLM provider.
	embProvider := r.cfg.Config.Memory.EmbeddingProvider
	if embProvider == "" {
		embProvider = os.Getenv("FORGE_EMBEDDING_PROVIDER")
	}
	if embProvider == "" {
		embProvider = mc.Provider
	}

	// Anthropic has no embedding API — skip.
	if embProvider == "anthropic" {
		r.logger.Info("primary provider is anthropic (no embedding API), trying fallbacks for embeddings", nil)
		// Try fallback providers.
		for _, fb := range mc.Fallbacks {
			if fb.Provider != "anthropic" {
				embProvider = fb.Provider
				break
			}
		}
		if embProvider == "anthropic" {
			r.logger.Info("no embedding-capable provider found, using keyword-only search", nil)
			return nil
		}
	}

	cfg := providers.OpenAIEmbedderConfig{
		APIKey: mc.Client.APIKey,
		Model:  r.cfg.Config.Memory.EmbeddingModel,
	}

	// Use the correct API key for the embedding provider if it differs from primary.
	if embProvider != mc.Provider {
		for _, fb := range mc.Fallbacks {
			if fb.Provider == embProvider {
				cfg.APIKey = fb.Client.APIKey
				cfg.BaseURL = fb.Client.BaseURL
				break
			}
		}
	}

	embedder, err := providers.NewEmbedder(embProvider, cfg)
	if err != nil {
		r.logger.Warn("failed to create embedder, using keyword-only search", map[string]any{
			"provider": embProvider,
			"error":    err.Error(),
		})
		return nil
	}

	return embedder
}

// overlaySecrets reads secrets from the configured provider chain and overlays
// them into envVars for known API key variables. Existing values are not overwritten.
func (r *Runner) overlaySecrets(envVars map[string]string) {
	provider := r.buildSecretProvider()
	if provider == nil {
		return
	}

	// Known secret keys to overlay into env for model resolution.
	knownKeys := []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"LLM_API_KEY",
		"MODEL_API_KEY",
		"TAVILY_API_KEY",
		"PERPLEXITY_API_KEY",
		"TELEGRAM_BOT_TOKEN",
		"SLACK_APP_TOKEN",
		"SLACK_BOT_TOKEN",
	}

	for _, key := range knownKeys {
		if envVars[key] != "" {
			continue // don't overwrite existing values
		}
		val, err := provider.Get(key)
		if err == nil {
			envVars[key] = val
			r.logger.Info("secret loaded", map[string]any{"key": key, "provider": provider.Name()})
		}
	}
}

// passphraseFromEnv returns a callback that reads the passphrase from FORGE_PASSPHRASE.
// Since run.go prompts interactively and sets the env var before calling into the
// runner, this callback will find the passphrase when a TTY is available.
func passphraseFromEnv() func() (string, error) {
	return func() (string, error) {
		if p := os.Getenv("FORGE_PASSPHRASE"); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("FORGE_PASSPHRASE not set")
	}
}

// buildSecretProvider creates a Provider from the config's secrets.providers list.
// Returns nil if no providers are configured (backward compat: default is env only,
// which is already handled by the env file loading).
func (r *Runner) buildSecretProvider() secrets.Provider {
	providerNames := r.cfg.Config.Secrets.Providers
	if len(providerNames) == 0 {
		return nil // no explicit secret providers configured
	}

	passCb := passphraseFromEnv()

	var providers []secrets.Provider
	for _, name := range providerNames {
		switch name {
		case "env":
			providers = append(providers, secrets.NewEnvProvider(""))
		case "encrypted-file":
			// Agent-local secrets file (in agent workdir)
			localPath := filepath.Join(r.cfg.WorkDir, ".forge", "secrets.enc")
			providers = append(providers, secrets.NewEncryptedFileProvider(localPath, passCb))

			// Global fallback secrets file
			home, err := os.UserHomeDir()
			if err == nil {
				globalPath := filepath.Join(home, ".forge", "secrets.enc")
				providers = append(providers, secrets.NewEncryptedFileProvider(globalPath, passCb))
			}
		default:
			r.logger.Warn("unknown secret provider, skipping", map[string]any{"provider": name})
		}
	}

	if len(providers) == 0 {
		return nil
	}
	if len(providers) == 1 {
		return providers[0]
	}
	return secrets.NewChainProvider(providers...)
}

// OverlaySecretsToEnv loads secrets from the config's provider chain and sets
// them in the OS environment so that channel adapters (which use os.Getenv) can
// access encrypted secrets. Only keys not already set in the env are written.
// workDir is the agent directory used to locate agent-local secrets.
func OverlaySecretsToEnv(cfg *types.ForgeConfig, workDir string) {
	providerNames := cfg.Secrets.Providers
	if len(providerNames) == 0 {
		return
	}

	passCb := passphraseFromEnv()

	var chain []secrets.Provider
	for _, name := range providerNames {
		switch name {
		case "encrypted-file":
			// Agent-local secrets file
			localPath := filepath.Join(workDir, ".forge", "secrets.enc")
			chain = append(chain, secrets.NewEncryptedFileProvider(localPath, passCb))

			// Global fallback secrets file
			home, err := os.UserHomeDir()
			if err == nil {
				globalPath := filepath.Join(home, ".forge", "secrets.enc")
				chain = append(chain, secrets.NewEncryptedFileProvider(globalPath, passCb))
			}
		case "env":
			// env provider uses os.Getenv — already available, skip
		}
	}

	if len(chain) == 0 {
		return
	}

	var provider secrets.Provider
	if len(chain) == 1 {
		provider = chain[0]
	} else {
		provider = secrets.NewChainProvider(chain...)
	}

	knownKeys := []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"LLM_API_KEY",
		"MODEL_API_KEY",
		"TAVILY_API_KEY",
		"PERPLEXITY_API_KEY",
		"TELEGRAM_BOT_TOKEN",
		"SLACK_APP_TOKEN",
		"SLACK_BOT_TOKEN",
	}

	for _, key := range knownKeys {
		if os.Getenv(key) != "" {
			continue
		}
		val, err := provider.Get(key)
		if err == nil && val != "" {
			_ = os.Setenv(key, val)
		}
	}
}

func defaultStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
