package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// emptyAssistantPlaceholder is the content substituted for an assistant
// message that came back with both empty content AND no tool calls —
// typically because the provider hit `finish_reason: length` (the
// model's max output token cap). Without this substitution the
// assistant turn would be invalid per the OpenAI chat-completions
// schema, and strict providers (Moonshot, hosted OpenRouter, OpenAI
// strict mode) reject every subsequent conversation that includes the
// turn — silently breaking every followup message in long-running
// channel threads via session recovery. See issue #131.
//
// The string is intentionally human-readable so an operator inspecting
// `.forge/sessions/<task>.json` understands the truncation. It is
// also the sentinel sanitizeMessages strips on session recovery, so
// both the persisted shape and the recovery path agree on what an
// "empty turn" looks like.
const emptyAssistantPlaceholder = "(continuing — previous response was truncated by output token limit)"

// ToolExecutor provides tool execution capabilities to the engine.
// The tools.Registry satisfies this interface via Go structural typing.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error)
	ToolDefinitions() []llm.ToolDefinition
}

// LLMExecutor implements AgentExecutor using an LLM client with tool calling.
type LLMExecutor struct {
	client             llm.Client
	tools              ToolExecutor
	hooks              *HookRegistry
	systemPrompt       string
	maxIter            int
	compactor          *Compactor
	store              *MemoryStore
	logger             Logger
	modelName          string        // resolved model name for context budget
	provider           string        // resolved provider name (anthropic, openai, ollama, custom)
	charBudget         int           // resolved character budget
	maxToolResultChars int           // computed from char budget
	filesDir           string        // directory for file_create output
	sessionMaxAge      time.Duration // max age for session recovery (0 = no limit)
	workflowPhases     []string      // workflow phases from skills (edit, finalize, query)
}

// LLMExecutorConfig configures the LLM executor.
type LLMExecutorConfig struct {
	Client         llm.Client
	Tools          ToolExecutor
	Hooks          *HookRegistry
	SystemPrompt   string
	MaxIterations  int
	Compactor      *Compactor
	Store          *MemoryStore
	Logger         Logger
	ModelName      string        // model name for context-aware budgeting
	Provider       string        // provider name (anthropic, openai, ollama, custom) — for audit attribution
	CharBudget     int           // explicit char budget override (0 = auto from model)
	FilesDir       string        // directory for file_create output (default: $TMPDIR/forge-files)
	SessionMaxAge  time.Duration // max idle time before session recovery is skipped (0 = 30m default)
	WorkflowPhases []string      // workflow phases from skills (edit, finalize, query)
}

// NewLLMExecutor creates a new LLMExecutor with the given configuration.
func NewLLMExecutor(cfg LLMExecutorConfig) *LLMExecutor {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = 50
	}
	hooks := cfg.Hooks
	if hooks == nil {
		hooks = NewHookRegistry()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = &nopLogger{}
	}

	// Resolve character budget from model name if not explicitly set.
	budget := cfg.CharBudget
	if budget == 0 {
		if cfg.ModelName != "" {
			budget = ContextBudgetForModel(cfg.ModelName)
		} else {
			budget = defaultContextTokens * charsPerToken
		}
	}

	// Tool result limit: 25% of char budget, floor 2K, cap 400K.
	toolLimit := budget / 4
	if toolLimit < 2_000 {
		toolLimit = 2_000
	}
	if toolLimit > 400_000 {
		toolLimit = 400_000
	}

	sessionMaxAge := cfg.SessionMaxAge
	if sessionMaxAge == 0 {
		sessionMaxAge = 30 * time.Minute
	}

	return &LLMExecutor{
		client:             cfg.Client,
		tools:              cfg.Tools,
		hooks:              hooks,
		systemPrompt:       cfg.SystemPrompt,
		maxIter:            maxIter,
		compactor:          cfg.Compactor,
		store:              cfg.Store,
		logger:             logger,
		modelName:          cfg.ModelName,
		provider:           cfg.Provider,
		charBudget:         budget,
		maxToolResultChars: toolLimit,
		filesDir:           cfg.FilesDir,
		sessionMaxAge:      sessionMaxAge,
		workflowPhases:     cfg.WorkflowPhases,
	}
}

// Execute processes a message through the LLM agent loop.
func (e *LLMExecutor) Execute(ctx context.Context, task *a2a.Task, msg *a2a.Message) (outMsg *a2a.Message, outErr error) {
	if e.filesDir != "" {
		ctx = WithFilesDir(ctx, e.filesDir)
	}

	// Phase 3 (#104) — open the agent-execution span. Parent (when
	// present) is the inbound dispatch span set by
	// forge-cli/server/a2a_server.go; otherwise this span is a root
	// (the scheduler, channel adapters, and other non-A2A entry
	// points run without a parent span). Final attributes
	// (iteration count, final state) are stamped just before End via
	// the deferred closure below — using named returns so the closure
	// can read the actual returned error without us having to mutate
	// finalState at every return site.
	ctx, span := Tracer().Start(ctx, "agent.execute")
	finalIter := 0
	defer func() {
		state := "completed"
		switch {
		case ctx.Err() != nil:
			// Cancellation / deadline propagated up — distinct from a
			// regular failure so the trace browser can group "operator
			// cancelled" separately from "agent crashed."
			state = "canceled"
		case outErr != nil:
			state = "failed"
			span.RecordError(outErr)
			span.SetStatus(codes.Error, outErr.Error())
		}
		span.SetAttributes(
			attribute.Int(observability.AttrForgeLoopIteration, finalIter),
			attribute.String(observability.AttrForgeTaskFinalState, state),
		)
		span.End()
	}()
	if task != nil && task.ID != "" {
		span.SetAttributes(attribute.String(observability.AttrForgeTaskID, task.ID))
	}
	if cid := CorrelationIDFromContext(ctx); cid != "" {
		span.SetAttributes(attribute.String(observability.AttrForgeCorrelationID, cid))
	}
	if e.provider != "" {
		span.SetAttributes(attribute.String(observability.AttrGenAISystem, e.provider))
	}
	if e.modelName != "" {
		span.SetAttributes(attribute.String(observability.AttrGenAIRequestModel, e.modelName))
	}

	mem := NewMemory(e.systemPrompt, e.charBudget, e.modelName)

	// Try to recover session from disk. If found, the disk snapshot
	// supersedes task.History to avoid duplicating messages.
	// Sessions older than sessionMaxAge are discarded to prevent stale
	// error context from poisoning the LLM (e.g., repeated tool failures
	// causing the LLM to stop retrying tools altogether).
	recovered := false
	if e.store != nil {
		saved, err := e.store.Load(task.ID)
		if err != nil {
			e.logger.Warn("failed to load session from disk", map[string]any{
				"task_id": task.ID, "error": err.Error(),
			})
		} else if saved != nil {
			if !saved.UpdatedAt.IsZero() && time.Since(saved.UpdatedAt) > e.sessionMaxAge {
				e.logger.Info("discarding stale session", map[string]any{
					"task_id":    task.ID,
					"updated_at": saved.UpdatedAt.Format(time.RFC3339),
					"max_age":    e.sessionMaxAge.String(),
				})
				_ = e.store.Delete(task.ID)
			} else {
				mem.LoadFromStore(saved)
				recovered = true
				e.logger.Info("session recovered from disk", map[string]any{
					"task_id":  task.ID,
					"messages": len(saved.Messages),
				})
			}
		}
	}

	// Load task history only if not recovered from disk.
	//
	// Issue #143 — the runner pre-appends params.Message to
	// task.History at the three tasks/send entry points
	// (runner.go:1174 / 1410 / 1672) so SSE observers see the
	// in-flight task with the inbound user message visible before
	// Execute completes. Execute treats task.History as PRIOR history
	// and *msg as the NEW message separately, so we strip the
	// trailing duplicate here to avoid emitting two consecutive
	// identical user turns into the LLM. Strict-mode providers
	// (gpt-5-nano and the other OpenAI reasoning models, Together's
	// Kimi gateway, ...) reject that shape and the executor returns
	// the canned "something went wrong" fallback.
	//
	// The `recovered` branch below has its own dedup logic against
	// the persisted memory; this is the symmetric guard for the
	// first-interaction path.
	if !recovered {
		historyToLoad := task.History
		if n := len(historyToLoad); n > 0 && a2aMessagesEqual(historyToLoad[n-1], *msg) {
			historyToLoad = historyToLoad[:n-1]
		}
		for _, histMsg := range historyToLoad {
			mem.Append(a2aMessageToLLM(histMsg))
		}
	}

	// Append the new user message, but skip if the recovered session
	// already ends with an identical user message (avoids duplicates
	// when users retry after a premature loop exit).
	newMsg := a2aMessageToLLM(*msg)
	if recovered {
		msgs := mem.Messages()
		// Find the last user message in the recovered session.
		lastUserIdx := -1
		for j := len(msgs) - 1; j >= 0; j-- {
			if msgs[j].Role == llm.RoleUser {
				lastUserIdx = j
				break
			}
		}
		if lastUserIdx < 0 || msgs[lastUserIdx].Content != newMsg.Content {
			mem.Append(newMsg)
		}
	} else {
		mem.Append(newMsg)
	}

	// Build tool definitions
	var toolDefs []llm.ToolDefinition
	if e.tools != nil {
		toolDefs = e.tools.ToolDefinitions()
	}

	// Track large tool outputs so they can be included as file parts
	// in the response (the LLM may truncate them due to output token limits).
	const largeToolOutputThreshold = 8000
	var largeToolOutputs []a2a.Part

	// stopNudgesSent tracks how many consecutive stop-nudges have been sent
	// since the LLM last made tool calls. Reset to 0 whenever the LLM calls
	// tools. This prevents infinite nudging while still allowing a second,
	// more forceful nudge when the workflow is clearly incomplete (e.g.,
	// commit failed but agent stopped anyway).
	stopNudgesSent := 0

	// toolsUsed tracks which tools were called during this execution.
	// Included in the continuation prompt so the LLM cannot hallucinate
	// actions it never performed.
	var toolsUsed []string

	// Workflow tracker detects behavioral patterns (exploration loops,
	// missing git ops) and injects proactive nudges. The agent never
	// sees iteration counts — nudges fire on consecutive read-only iterations.
	tracker := newWorkflowTracker(e.workflowPhases)

	// Pre-compute available write tools for nudge messages.
	var availWriteTools []string
	for _, td := range toolDefs {
		if isWriteActionTool(td.Function.Name) {
			availWriteTools = append(availWriteTools, td.Function.Name)
		}
	}

	// Agent loop
	for i := 0; i < e.maxIter; i++ {
		// Record iteration count on the outer span — the closure stamps
		// the final value before End.
		finalIter = i + 1
		// Honor cancellation at iteration boundary. Returning ctx.Err()
		// here propagates context.Canceled / DeadlineExceeded up to the
		// runner, which maps it to TaskStateCanceled +
		// invocation_cancelled audit. See issue #88 / FWS-4.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Run compaction before LLM call (best-effort).
		if e.compactor != nil {
			if _, err := e.compactor.MaybeCompact(task.ID, mem); err != nil {
				e.logger.Warn("compaction error", map[string]any{
					"task_id": task.ID, "error": err.Error(),
				})
			}
		}

		messages := mem.Messages()

		// Fire BeforeLLMCall hook
		if err := e.hooks.Fire(ctx, BeforeLLMCall, &HookContext{
			Messages:      messages,
			TaskID:        TaskIDFromContext(ctx),
			CorrelationID: CorrelationIDFromContext(ctx),
		}); err != nil {
			return nil, fmt.Errorf("before LLM call hook: %w", err)
		}

		// Call LLM
		req := &llm.ChatRequest{
			Messages: messages,
			Tools:    toolDefs,
		}

		// Capture wall-clock duration of the provider call so the
		// AfterLLMCall hook can stamp duration_ms on the llm_call audit
		// event and X-Forge-Duration-Ms header. See issue #87 / FWS-3.
		llmStart := time.Now()
		// Phase 3 (#104) — child span around the provider call. The
		// span carries the same gen_ai.* attributes the audit event
		// does so a backend can join the two by trace_id without a
		// translation table. Span ends right after the response is
		// observed (before AfterLLMCall hook) so the hook runs in the
		// parent span's context — the hook is a Forge-internal step,
		// not part of the LLM call's wall-clock measurement.
		llmCtx, llmSpan := Tracer().Start(ctx, "llm.completion")
		llmSpan.SetAttributes(
			attribute.String(observability.AttrGenAISystem, e.provider),
			attribute.String(observability.AttrGenAIRequestModel, e.modelName),
		)
		resp, err := e.client.Chat(llmCtx, req)
		llmDuration := time.Since(llmStart)
		if err != nil {
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, err.Error())
			llmSpan.End()
			// Cancellation is not a "something went wrong." When ctx was
			// cancelled, the provider's error (whatever its concrete type
			// — net/http wraps inconsistently across DNS / TLS / body
			// paths, and errors.Is(err, context.Canceled) is unreliable
			// against the chain it produces) is by definition caused by
			// the cancellation. Propagating ctx.Err() makes executeTask
			// route to invocation_cancelled instead of state=failed.
			// See issue #88 / FWS-4.
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			_ = e.hooks.Fire(ctx, OnError, &HookContext{
				Error:           err,
				TaskID:          TaskIDFromContext(ctx),
				CorrelationID:   CorrelationIDFromContext(ctx),
				LLMCallDuration: llmDuration,
				Provider:        e.provider,
				Model:           e.modelName,
			})
			// Return user-friendly error (raw error is already logged via OnError hook)
			return nil, fmt.Errorf("something went wrong while processing your request, please try again")
		}
		// Happy-path: stamp usage + finish_reason from the response,
		// then close the span. Doing this BEFORE the AfterLLMCall hook
		// keeps the hook's redaction / audit work outside the LLM
		// span's duration — the span is the provider call alone.
		llmSpan.SetAttributes(
			attribute.Int(observability.AttrGenAIUsageInputTokens, resp.Usage.InputTokens),
			attribute.Int(observability.AttrGenAIUsageOutputTokens, resp.Usage.OutputTokens),
		)
		if resp.FinishReason != "" {
			llmSpan.SetAttributes(attribute.StringSlice(observability.AttrGenAIResponseFinishReasons, []string{resp.FinishReason}))
		}
		llmSpan.End()

		// Fire AfterLLMCall hook
		if err := e.hooks.Fire(ctx, AfterLLMCall, &HookContext{
			Messages:        messages,
			Response:        resp,
			TaskID:          TaskIDFromContext(ctx),
			CorrelationID:   CorrelationIDFromContext(ctx),
			LLMCallDuration: llmDuration,
			Provider:        e.provider,
			Model:           e.modelName,
		}); err != nil {
			return nil, fmt.Errorf("after LLM call hook: %w", err)
		}

		// Append assistant message to memory.
		//
		// Issue #131 — when the LLM hits finish_reason=length (or otherwise
		// returns empty content) AND made no tool calls, the assistant turn
		// has neither content nor tool_calls. That shape is invalid per the
		// OpenAI chat-completions schema; strict providers (Moonshot,
		// hosted OpenRouter, OpenAI strict mode) reject any subsequent
		// conversation that includes such a turn with HTTP 400. The in-loop
		// recovery below at "If the LLM returned empty text after executing
		// tools" papers over it for the current task, but `persistSession`
		// at the end of Execute writes the polluted memory to disk — and
		// the next request that recovers this session hits the strict
		// validator and returns "something went wrong" to every followup.
		//
		// Substitute a placeholder content string instead of skipping the
		// Append entirely. Skipping would break the assistant↔user turn
		// pairing the empty-content recovery branch relies on (it appends
		// a user nudge that must follow an assistant turn). The placeholder
		// is non-empty so strict validators accept the message, and it
		// names the failure mode so an operator scanning a recovered
		// session understands what happened.
		assistantMsg := resp.Message
		if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 {
			assistantMsg.Content = emptyAssistantPlaceholder
		}
		mem.Append(assistantMsg)

		// Check if we're done: the definitive signal is the absence of tool
		// calls. FinishReason is unreliable — some providers return "stop"
		// even when tool calls are present, and others return empty/non-standard
		// values. Only the tool call list determines whether execution continues.
		if len(resp.Message.ToolCalls) == 0 {
			// If the LLM stopped after executing tools, send a continuation
			// nudge. This catches cases where the LLM reports findings instead
			// of completing the full workflow (e.g., stops after exploration
			// without editing/committing/pushing). The maxIter limit prevents
			// infinite loops.
			if i > 0 {
				// Determine if the workflow is incomplete based on required phases.
				workflowIncomplete := false
				if tracker.requireEdit && !tracker.phaseOK(phaseEdit) {
					workflowIncomplete = true
				}
				if tracker.requireFinalize && !tracker.phaseOK(phaseGitOps) {
					workflowIncomplete = true
				}

				// Determine nudge budget:
				// - No workflow phases configured → 1 nudge (can't tell if done)
				// - Workflow phases configured and ALL complete → 0 (agent is done)
				// - Workflow incomplete, no errors → 1 nudge
				// - Workflow incomplete with git errors → 2 nudges
				hasWorkflowRequirements := tracker.requireEdit || tracker.requireFinalize
				maxNudges := 1 // default for agents without workflow phases
				if hasWorkflowRequirements && !workflowIncomplete {
					maxNudges = 0 // workflow is complete — don't nudge
				} else if workflowIncomplete && tracker.phaseHasError[phaseGitOps] {
					maxNudges = 2
				} else if !hasWorkflowRequirements && !tracker.phaseSeen[phaseEdit] && !tracker.phaseSeen[phaseGitOps] {
					// Informational / Q&A conversation — agent only used
					// explore-phase tools (web_search, file_read, etc.) and
					// gave a text response. No code changes were attempted,
					// so there's nothing to "continue" with.
					maxNudges = 0
				}

				if stopNudgesSent < maxNudges {
					stopNudgesSent++

					// Workflow-aware stop-point nudge: check what phases
					// the agent completed successfully before stopping.
					var nudge string
					if stopNudgesSent == 2 {
						// Second nudge: agent stopped again without calling
						// tools despite knowing the task isn't done. Be very
						// forceful.
						nudge = "You stopped AGAIN without calling any tools. " +
							"Do NOT describe what needs to be done — DO it. " +
							"Call the required tools NOW: "
						var steps []string
						if tracker.requireEdit && !tracker.phaseOK(phaseEdit) {
							steps = append(steps, strings.Join(availWriteTools, "/")+
								" to fix the code")
						}
						if tracker.requireFinalize && !tracker.phaseOK(phaseGitOps) {
							if tracker.phaseHasError[phaseGitOps] {
								steps = append(steps, "github_commit (previous attempt FAILED — check the files parameter is a JSON array)")
							}
							steps = append(steps, "github_push -> github_create_pr")
						}
						if len(steps) > 0 {
							nudge += strings.Join(steps, ", then ") + "."
						} else {
							nudge += "complete the remaining steps."
						}
					} else if tracker.requireEdit && !tracker.phaseSeen[phaseEdit] {
						// Never wrote anything — stuck in exploration
						nudge = "You stopped without making any code changes. " +
							"You called: " + strings.Join(dedup(toolsUsed), ", ") + ". " +
							"You MUST continue: "
						if tracker.requireEdit {
							nudge += "edit the code"
						}
						if tracker.requireFinalize {
							nudge += ", then commit, push, and create PR"
						}
						nudge += ". Available write tools: " + strings.Join(availWriteTools, ", ") + "."
					} else if tracker.requireEdit && tracker.phaseSeen[phaseEdit] && tracker.requireFinalize && !tracker.phaseOK(phaseGitOps) {
						// Edited but git ops either missing or had errors
						nudge = "You edited files but "
						if tracker.phaseHasError[phaseGitOps] {
							nudge += "some git operations FAILED. Fix the errors and retry: "
						} else {
							nudge += "stopped before git operations. Complete NOW: "
						}
						nudge += "github_status -> github_commit -> github_push -> github_create_pr."
					} else {
						// Standard: completed or no requirements
						nudge = "You stopped. If the task is complete, summarize what was done. " +
							"If not, continue with the remaining steps."
					}
					e.logger.Info("sending continuation nudge", map[string]any{
						"task_id":     TaskIDFromContext(ctx),
						"iteration":   i,
						"tools_used":  strings.Join(toolsUsed, ", "),
						"has_edits":   tracker.phaseSeen[phaseEdit],
						"has_git":     tracker.phaseSeen[phaseGitOps],
						"git_errors":  tracker.phaseHasError[phaseGitOps],
						"nudge_count": stopNudgesSent,
						"max_nudges":  maxNudges,
					})
					mem.Append(llm.ChatMessage{
						Role:    llm.RoleUser,
						Content: nudge,
					})
					continue
				}
			}

			// If the LLM returned empty text after executing tools, re-prompt
			// it once to produce a meaningful summary instead of sending nothing.
			if strings.TrimSpace(resp.Message.Content) == "" && i > 0 {
				mem.Append(llm.ChatMessage{
					Role:    llm.RoleUser,
					Content: "Your response was empty. Please provide a brief summary of what you found, what you were unable to do, and suggest next steps.",
				})
				retryReq := &llm.ChatRequest{
					Messages: mem.Messages(),
				}
				retryStart := time.Now()
				if retryResp, retryErr := e.client.Chat(ctx, retryReq); retryErr == nil && strings.TrimSpace(retryResp.Message.Content) != "" {
					resp = retryResp
					mem.Append(resp.Message)
					// Fire AfterLLMCall so audit + headers capture the retry's
					// usage/duration alongside the original turn.
					_ = e.hooks.Fire(ctx, AfterLLMCall, &HookContext{
						Messages:        mem.Messages(),
						Response:        retryResp,
						TaskID:          TaskIDFromContext(ctx),
						CorrelationID:   CorrelationIDFromContext(ctx),
						LLMCallDuration: time.Since(retryStart),
						Provider:        e.provider,
						Model:           e.modelName,
					})
				}
			}
			if strings.TrimSpace(resp.Message.Content) == "" {
				resp.Message.Content = "I processed your request but wasn't able to produce a response. Please try again."
			}
			e.persistSession(task.ID, mem)
			return e.finalizeResponse(ctx, resp.Message, largeToolOutputs...), nil
		}

		// Execute tool calls
		if e.tools == nil {
			if strings.TrimSpace(resp.Message.Content) == "" {
				resp.Message.Content = "I processed your request but wasn't able to produce a response. Please try again."
			}
			e.persistSession(task.ID, mem)
			return e.finalizeResponse(ctx, resp.Message, largeToolOutputs...), nil
		}

		// The LLM made tool calls -- it's making progress. Allow
		// another nudge if it stops again after this round.
		stopNudgesSent = 0

		iterResults := make([]toolIterResult, 0, len(resp.Message.ToolCalls))

		for _, tc := range resp.Message.ToolCalls {
			// Honor cancellation between tool calls within an iteration —
			// orchestrators that cancel mid-iteration get fast exit
			// without burning more LLM/tool spend. See issue #88 / FWS-4.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			toolsUsed = append(toolsUsed, tc.Function.Name)

			// Fire BeforeToolExec hook
			if err := e.hooks.Fire(ctx, BeforeToolExec, &HookContext{
				ToolName:      tc.Function.Name,
				ToolInput:     tc.Function.Arguments,
				TaskID:        TaskIDFromContext(ctx),
				CorrelationID: CorrelationIDFromContext(ctx),
			}); err != nil {
				return nil, fmt.Errorf("before tool exec hook: %w", err)
			}

			// Execute tool. Capture wall-clock duration so the
			// AfterToolExec hook can stamp duration_ms on the tool_exec
			// audit event. See issue #87 / FWS-3.
			toolStart := time.Now()
			// Phase 3 (#104) — child span around the tool call. Span
			// name is "tool.<tool_name>" so a flame graph groups tools
			// by kind without a query. Tool args / results are NOT
			// recorded as attributes here — Phase 3 is metadata-only;
			// content capture lands when the FWS-8 redactor can be
			// reused for spans.
			toolCtx, toolSpan := Tracer().Start(ctx, "tool."+tc.Function.Name)
			toolSpan.SetAttributes(attribute.String(observability.AttrForgeToolName, tc.Function.Name))
			result, execErr := e.tools.Execute(toolCtx, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			toolDuration := time.Since(toolStart)
			if execErr != nil {
				toolSpan.RecordError(execErr)
				toolSpan.SetStatus(codes.Error, execErr.Error())
				toolSpan.SetAttributes(attribute.String(observability.AttrForgeToolError, execErr.Error()))
				result = fmt.Sprintf("Error executing tool %s: %s", tc.Function.Name, execErr.Error())
			}
			toolSpan.End()
			iterResults = append(iterResults, toolIterResult{
				Name:     tc.Function.Name,
				Failed:   execErr != nil,
				FilePath: extractReadFilePath(tc.Function.Name, tc.Function.Arguments),
			})

			// Truncate oversized tool results to avoid LLM API errors.
			// Limit is proportional to model context budget (25%, floor 2K, cap 400K).
			if len(result) > e.maxToolResultChars {
				result = result[:e.maxToolResultChars] + "\n\n[OUTPUT TRUNCATED -- original length: " + strconv.Itoa(len(result)) + " chars]"
			}

			// Fire AfterToolExec hook -- hooks may redact ToolOutput.
			afterHctx := &HookContext{
				ToolName:         tc.Function.Name,
				ToolInput:        tc.Function.Arguments,
				ToolOutput:       result,
				Error:            execErr,
				TaskID:           TaskIDFromContext(ctx),
				CorrelationID:    CorrelationIDFromContext(ctx),
				ToolExecDuration: toolDuration,
			}
			if err := e.hooks.Fire(ctx, AfterToolExec, afterHctx); err != nil {
				return nil, fmt.Errorf("after tool exec hook: %w", err)
			}
			result = afterHctx.ToolOutput // allow hooks to redact output

			// Handle file_create tool: always create a file part.
			// For other tools with large output, detect content type.
			// Skip cli_execute: it's an intermediate tool — the LLM should
			// analyze its output and produce a human-readable response, not
			// forward raw JSON. Attaching cli_execute output as a file causes
			// the LLM to say "see attached" instead of writing a report.
			if tc.Function.Name == "file_create" {
				var fc struct {
					Filename string `json:"filename"`
					Content  string `json:"content"`
					MimeType string `json:"mime_type"`
				}
				if err := json.Unmarshal([]byte(result), &fc); err == nil && fc.Filename != "" {
					largeToolOutputs = append(largeToolOutputs, a2a.Part{
						Kind: a2a.PartKindFile,
						File: &a2a.FileContent{
							Name:     fc.Filename,
							MimeType: fc.MimeType,
							Bytes:    []byte(fc.Content),
						},
					})
				}
			} else if tc.Function.Name != "cli_execute" && len(result) > largeToolOutputThreshold {
				name, mime := detectFileType(result, tc.Function.Name)
				largeToolOutputs = append(largeToolOutputs, a2a.Part{
					Kind: a2a.PartKindFile,
					File: &a2a.FileContent{
						Name:     name,
						MimeType: mime,
						Bytes:    []byte(result),
					},
				})
			}

			// Append tool result to memory
			mem.Append(llm.ChatMessage{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}

		// Record this iteration's tools for workflow tracking.
		tracker.recordIteration(iterResults)

		// Proactive mid-loop nudge (fires while agent is still calling tools).
		if nudgeMsg, shouldNudge := tracker.generateProactiveNudge(availWriteTools); shouldNudge {
			e.logger.Info("sending proactive workflow nudge", map[string]any{
				"task_id":           TaskIDFromContext(ctx),
				"iteration":         i,
				"consecutive_reads": tracker.consecutiveReads,
			})
			mem.Append(llm.ChatMessage{Role: llm.RoleUser, Content: nudgeMsg})
		}
	}

	e.persistSession(task.ID, mem)
	return nil, fmt.Errorf("agent loop exceeded maximum iterations (%d)", e.maxIter)
}

// persistSession saves the current memory state to disk (best-effort).
// It strips orphaned tool calls from the last assistant message to prevent
// the Responses API from rejecting recovered sessions with
// "No tool output found for function call".
func (e *LLMExecutor) persistSession(taskID string, mem *Memory) {
	if e.store == nil {
		return
	}
	mem.mu.Lock()
	msgs := make([]llm.ChatMessage, len(mem.messages))
	copy(msgs, mem.messages)
	mem.mu.Unlock()

	// Build a set of tool call IDs that have matching tool results.
	answeredCalls := make(map[string]bool)
	for _, m := range msgs {
		if m.Role == llm.RoleTool && m.ToolCallID != "" {
			answeredCalls[m.ToolCallID] = true
		}
	}

	// Strip unanswered tool calls from assistant messages to avoid
	// orphaned function_call items in the persisted session.
	for i := range msgs {
		if msgs[i].Role != llm.RoleAssistant || len(msgs[i].ToolCalls) == 0 {
			continue
		}
		var kept []llm.ToolCall
		for _, tc := range msgs[i].ToolCalls {
			if answeredCalls[tc.ID] {
				kept = append(kept, tc)
			}
		}
		if len(kept) != len(msgs[i].ToolCalls) {
			msgs[i].ToolCalls = kept
		}
	}

	data := &SessionData{
		TaskID:   taskID,
		Messages: msgs,
		Summary:  mem.existingSummary,
	}

	if err := e.store.Save(data); err != nil {
		e.logger.Warn("failed to persist session", map[string]any{
			"task_id": taskID, "error": err.Error(),
		})
	}
}

// ExecuteStream runs the tool-calling loop non-streaming, then emits the final
// response as a single message on the channel. True word-by-word streaming is v2.
func (e *LLMExecutor) ExecuteStream(ctx context.Context, task *a2a.Task, msg *a2a.Message) (<-chan *a2a.Message, error) {
	ch := make(chan *a2a.Message, 1)
	go func() {
		defer close(ch)
		resp, err := e.Execute(ctx, task, msg)
		if err != nil {
			ch <- &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: []a2a.Part{a2a.NewTextPart("Error: " + err.Error())},
			}
			return
		}
		ch <- resp
	}()
	return ch, nil
}

// Close is a no-op for LLMExecutor.
func (e *LLMExecutor) Close() error { return nil }

// a2aMessageToLLM converts an A2A message to an LLM chat message.
func a2aMessageToLLM(msg a2a.Message) llm.ChatMessage {
	role := llm.RoleUser
	if msg.Role == a2a.MessageRoleAgent {
		role = llm.RoleAssistant
	}

	var textParts []string
	for _, p := range msg.Parts {
		if p.Kind == a2a.PartKindText && p.Text != "" {
			textParts = append(textParts, p.Text)
		}
	}

	return llm.ChatMessage{
		Role:    role,
		Content: strings.Join(textParts, "\n"),
	}
}

// a2aMessagesEqual reports whether two A2A messages have the same role
// and the same concatenated text content. Used by Execute's history-
// load path (issue #143) to detect the runner's pre-appended
// params.Message in task.History and avoid emitting it as a duplicate
// alongside the *msg argument.
//
// Only the LLM-visible projection matters here (role + text content) —
// the message envelope's part-kind enumeration and metadata are
// irrelevant to whether the LLM sees a duplicate user turn.
func a2aMessagesEqual(a, b a2a.Message) bool {
	if a.Role != b.Role {
		return false
	}
	return a2aMessageToLLM(a).Content == a2aMessageToLLM(b).Content
}

// detectFileType inspects tool output content and returns an appropriate
// filename and MIME type. JSON and YAML content gets typed extensions;
// everything else defaults to markdown.
func detectFileType(content, toolName string) (filename, mimeType string) {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		// Quick check: try to parse as JSON.
		if json.Valid([]byte(trimmed)) {
			return toolName + "-output.json", "application/json"
		}
	}
	if strings.HasPrefix(trimmed, "---") {
		return toolName + "-output.yaml", "text/yaml"
	}
	return toolName + "-output.md", "text/markdown"
}

// llmMessageToA2A converts an LLM chat message to an A2A message.
// Any extra parts (e.g. large tool output files) are appended after the text part.
func llmMessageToA2A(msg llm.ChatMessage, extraParts ...a2a.Part) *a2a.Message {
	role := a2a.MessageRoleAgent
	if msg.Role == llm.RoleUser {
		role = a2a.MessageRoleUser
	}

	parts := []a2a.Part{a2a.NewTextPart(msg.Content)}
	parts = append(parts, extraParts...)

	return &a2a.Message{
		Role:  role,
		Parts: parts,
	}
}

// finalizeResponse builds the A2A message for the final agent response and,
// when the LLM body itself is long enough to be split into "inline summary +
// attached markdown" by channel adapters, asks the LLM for a real summary.
// The summary is attached as Message.Summary; channels prefer it over
// head-truncating the verbose body. Failure to produce a summary is non-fatal
// — channels fall back to their existing head-truncation behavior.
//
// When the LLM text is short but a tool produced a large file part, no
// summariser pass is needed: the LLM text is already the summary of the file
// content, and channel adapters already use it that way.
func (e *LLMExecutor) finalizeResponse(ctx context.Context, msg llm.ChatMessage, extraParts ...a2a.Part) *a2a.Message {
	out := llmMessageToA2A(msg, extraParts...)
	if len(msg.Content) > summaryInlineThreshold {
		out.Summary = generateSummary(ctx, e.client, msg.Content)
	}
	return out
}

// isWriteActionTool returns true for tools that modify state (edit, write,
// commit, push, create PR) as opposed to read-only tools (read, grep, glob,
// directory_tree, clone, status).
func isWriteActionTool(name string) bool {
	switch name {
	case "code_agent_edit", "code_agent_write", "code_agent_patch",
		"github_commit", "github_push", "github_create_pr",
		"github_checkout", "github_create_issue",
		"file_create", "code_agent_run":
		return true
	}
	// Catch any tool with "edit", "write", "commit", "push" in the name.
	lower := strings.ToLower(name)
	for _, kw := range []string{"edit", "write", "commit", "push", "patch", "create"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// ─── Workflow Tracker ────────────────────────────────────────────────

// workflowPhase classifies tools by their role in the coding workflow.
type workflowPhase int

const (
	phaseSetup   workflowPhase = iota // clone, scaffold
	phaseExplore                      // read, grep, glob, tree, read_skill
	phaseEdit                         // edit, write, patch
	phaseGitOps                       // status, commit, push, create_pr
)

// toolIterResult captures a tool call's name, whether it failed, and
// (for read tools) the file path that was read.
type toolIterResult struct {
	Name     string
	Failed   bool
	FilePath string // non-empty for file_read / code_agent_read
}

// workflowTracker monitors agent behavior to detect exploration loops
// and missing workflow phases. The agent never sees iteration counts.
type workflowTracker struct {
	phaseSeen          map[workflowPhase]bool
	phaseHasError      map[workflowPhase]bool // at least one tool in this phase errored
	consecutiveReads   int                    // resets when a non-explore tool is called
	totalReadIters     int
	itersSinceLastEdit int // iterations since last edit-phase tool
	planCheckpointDone bool
	transitionDone     bool
	urgentDone         bool
	gitNudgeDone       bool
	verifyNudgeDone    bool           // post-edit verification nudge (fires once)
	requireEdit        bool           // skill(s) declared workflow_phase: edit
	requireFinalize    bool           // skill(s) declared workflow_phase: finalize
	fileReadCounts     map[string]int // path → read count (for re-read detection)
	rereadNudgeDone    bool           // fires once per re-read batch
}

func newWorkflowTracker(phases []string) *workflowTracker {
	wt := &workflowTracker{
		phaseSeen:      make(map[workflowPhase]bool),
		phaseHasError:  make(map[workflowPhase]bool),
		fileReadCounts: make(map[string]int),
	}
	for _, p := range phases {
		switch p {
		case "edit":
			wt.requireEdit = true
		case "finalize":
			wt.requireFinalize = true
		}
	}
	return wt
}

// phaseOK returns true if the phase was seen AND had no errors.
func (wt *workflowTracker) phaseOK(p workflowPhase) bool {
	return wt.phaseSeen[p] && !wt.phaseHasError[p]
}

// toolPhase classifies a tool name into a workflow phase.
func toolPhase(name string) workflowPhase {
	switch name {
	case "github_clone", "code_agent_scaffold", "github_checkout":
		return phaseSetup
	case "code_agent_read", "grep_search", "glob_search", "directory_tree", "read_skill", "github_status",
		"github_list_prs", "github_get_user", "github_list_stargazers", "github_list_forks",
		"github_pr_author_profiles", "github_stargazer_profiles":
		return phaseExplore
	case "code_agent_edit", "code_agent_write", "code_agent_patch", "file_create", "code_agent_run":
		return phaseEdit
	case "github_commit", "github_push", "github_create_pr":
		return phaseGitOps
	}
	// Keyword fallback
	lower := strings.ToLower(name)
	for _, kw := range []string{"read", "grep", "glob", "search", "tree", "status"} {
		if strings.Contains(lower, kw) {
			return phaseExplore
		}
	}
	for _, kw := range []string{"edit", "write", "patch", "create"} {
		if strings.Contains(lower, kw) {
			return phaseEdit
		}
	}
	for _, kw := range []string{"commit", "push"} {
		if strings.Contains(lower, kw) {
			return phaseGitOps
		}
	}
	return phaseSetup // default: setup / unknown
}

// recordIteration updates the tracker based on which tools were called and
// whether they succeeded or failed. Failed tools mark phaseHasError but still
// mark phaseSeen (the tool was attempted). The phaseOK() method checks both.
func (wt *workflowTracker) recordIteration(results []toolIterResult) {
	allExplore := true
	for _, r := range results {
		phase := toolPhase(r.Name)
		wt.phaseSeen[phase] = true
		if r.Failed {
			wt.phaseHasError[phase] = true
		}
		if phase != phaseExplore {
			allExplore = false
		}
		// Track file reads for re-read detection.
		if r.FilePath != "" && !r.Failed {
			wt.fileReadCounts[r.FilePath]++
		}
	}

	if allExplore && len(results) > 0 {
		wt.consecutiveReads++
		wt.totalReadIters++
	} else {
		wt.consecutiveReads = 0
	}

	// Track iterations since last edit
	hasEdit := false
	for _, r := range results {
		if toolPhase(r.Name) == phaseEdit && !r.Failed {
			hasEdit = true
			break
		}
	}
	if hasEdit {
		wt.itersSinceLastEdit = 0
	} else {
		wt.itersSinceLastEdit++
	}
}

// generateProactiveNudge returns a behavioral nudge if the agent is stuck in
// an exploration loop. Nudges escalate monotonically — each tier fires once.
func (wt *workflowTracker) generateProactiveNudge(availWriteTools []string) (string, bool) {
	// Re-read detection nudge: highest priority — fires once when any file
	// has been read 2+ times, which wastes context and triggers compaction.
	if !wt.rereadNudgeDone {
		var rereadFiles []string
		for path, count := range wt.fileReadCounts {
			if count >= 2 {
				rereadFiles = append(rereadFiles, path)
			}
		}
		if len(rereadFiles) > 0 {
			wt.rereadNudgeDone = true
			return "STOP RE-READING FILES: You have already read " +
				strings.Join(rereadFiles, ", ") + " earlier in this session. " +
				"The content was lost to compaction. Do NOT read the entire file again — " +
				"that will trigger more compaction and lose context again. Instead: " +
				"1) State your hypothesis based on what you learned. " +
				"2) If you need specific lines, use offset/limit parameters. " +
				"3) Proceed to edit based on your current knowledge.", true
		}
	}

	// Git workflow nudge: only if finalize is required
	if wt.requireFinalize && wt.phaseOK(phaseEdit) && !wt.phaseOK(phaseGitOps) && wt.itersSinceLastEdit >= 4 && !wt.gitNudgeDone {
		wt.gitNudgeDone = true
		nudge := "You edited files but haven't committed. "
		if wt.requireEdit && wt.verifyNudgeDone {
			nudge += "BEFORE committing: does your edit change RUNTIME behavior, not just types or tests? " +
				"Does the failing input now reach a code path that handles it correctly? " +
				"If your edit only modifies test files, it does NOT fix the bug — edit source code first. "
		}
		nudge += "Complete the git workflow: " +
			"github_status -> github_commit -> github_push -> github_create_pr."
		return nudge, true
	}

	// Post-edit verification nudge: fires once immediately after first edit in bug-fix workflows.
	if wt.requireEdit && wt.phaseOK(phaseEdit) && (!wt.requireFinalize || !wt.phaseOK(phaseGitOps)) && !wt.verifyNudgeDone && wt.itersSinceLastEdit == 1 {
		wt.verifyNudgeDone = true
		return "VERIFY YOUR FIX: You just edited code. Before committing, trace the failing input through your new code: " +
			"1) What value was causing the bug (e.g., an object, null, wrong type)? " +
			"2) Does that value now reach a code path that handles it correctly? " +
			"3) Read the functions your new code calls — do they accept that input type? " +
			"If the fix only adds types or annotations without changing runtime behavior, it is wrong. " +
			"If correct, proceed to commit.", true
	}

	// Exploration loop nudges: only if edit is required
	if wt.requireEdit {
		if wt.phaseOK(phaseEdit) {
			return "", false
		}

		if wt.consecutiveReads >= 8 && !wt.urgentDone {
			wt.urgentDone = true
			return "STOP READING. You have explored " + fmt.Sprintf("%d", wt.consecutiveReads) +
				" consecutive iterations without a single edit. Act on what you know NOW. " +
				"Call " + strings.Join(availWriteTools, "/") + " immediately. An imperfect fix is better than endless exploration.", true
		}

		if wt.consecutiveReads >= 6 && !wt.transitionDone {
			wt.transitionDone = true
			return "You have been exploring for " + fmt.Sprintf("%d", wt.consecutiveReads) +
				" consecutive iterations without making changes. " +
				"If fixing a bug, have you traced it to its origin? Have you read the functions you plan to change? " +
				"If not, do those reads now. Otherwise, Start editing with " + strings.Join(availWriteTools, ", ") + ". " +
				"An imperfect edit you can iterate on is better than more reading.", true
		}

		if wt.consecutiveReads >= 4 && !wt.planCheckpointDone {
			wt.planCheckpointDone = true
			return "PLANNING CHECKPOINT: You've read " + fmt.Sprintf("%d", wt.totalReadIters) +
				" files without editing. Before reading more: " +
				"1) If fixing a bug: have you traced the error to its origin, not just where it surfaces? " +
				"2) Have you read the implementation of every function you plan to call or replace? " +
				"3) If yes, state your fix and call " + strings.Join(availWriteTools, "/") + ". " +
				"If no, do those reads next — then edit.", true
		}

		return "", false
	}

	// Query-only: gentle nudge at 8 consecutive reads
	if !wt.requireEdit && !wt.requireFinalize {
		if wt.consecutiveReads >= 8 && !wt.urgentDone {
			wt.urgentDone = true
			return "You have been reading for " + fmt.Sprintf("%d", wt.consecutiveReads) +
				" consecutive iterations. If you have enough information, provide your analysis. " +
				"If not, focus your remaining searches.", true
		}
	}

	return "", false
}

// extractReadFilePath extracts the file path from tool arguments for
// file_read and code_agent_read tools. Returns "" for other tools or
// if the path cannot be extracted.
func extractReadFilePath(toolName, argsJSON string) string {
	switch toolName {
	case "file_read", "code_agent_read":
	default:
		return ""
	}
	var args struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	if args.FilePath != "" {
		return args.FilePath
	}
	return args.Path
}

// dedup returns unique tool names in first-seen order.
func dedup(names []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
