package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/richardsondx/IronLark/internal/agent"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/graph"
	"github.com/richardsondx/IronLark/internal/memory"
	"github.com/richardsondx/IronLark/internal/models"
	"github.com/richardsondx/IronLark/internal/ops"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/sessions"
	"github.com/richardsondx/IronLark/internal/state"
	"github.com/richardsondx/IronLark/internal/threads"
)

var errTurnStopped = errors.New("turn stopped")

type Engine struct {
	Runtime     state.Runtime
	Collector   *ctxpkg.Collector
	Executor    *executor.Executor
	Provider    provider.Client
	Renderer    render.UI
	Agents      agent.Store
	Sessions    sessions.Store
	Threads     threads.Store
	PolicyStore policy.Store
	Graph       *graph.Manager
	Ops         *ops.Manager
}

type turnBudget struct {
	Soft int
	Hard int
}

type completionTracker struct {
	budget            turnBudget
	executedActions   bool
	noProgressStreak  int
	lastActionKey     string
	lastResultKey     string
	lastReplyKey      string
	lastStage         string
	extensionNarrated bool
}

type continuationDecision struct {
	Continue         bool
	Continuation     *core.ConversationMessage
	Status           core.CompletionStatus
	AnnounceExtended bool
	AnnounceHardCap  bool
}

func (e *Engine) RunTask(ctx context.Context, prompt string, stdin []byte, mode string) error {
	if e.Provider == nil {
		return fmt.Errorf("provider is not configured or API key is unavailable")
	}

	snapshot, err := e.Collector.CollectMinimal(ctx, e.Runtime, stdin)
	if err != nil {
		return err
	}
	graphDigest := e.ensureGraphDigest(ctx, graph.ModeLight)
	opsDigest := e.ensureOpsDigest()

	threadRef, threadState, history, err := e.prepareTaskHistory(prompt, snapshot, graphDigest, opsDigest)
	if err != nil {
		return err
	}

	record := sessions.Record{
		ID:           newRecordID(),
		Mode:         mode,
		Prompt:       prompt,
		Provider:     e.Runtime.ProviderName,
		Model:        e.Runtime.Model,
		ApprovalMode: e.Runtime.ApprovalMode,
		ContextJSON:  snapshot.JSON(),
		StartedAt:    time.Now().UTC(),
	}

	budget := resolveTurnBudget(e.Runtime.Config.Tools.SoftTurns, e.Runtime.Config.Tools.MaxTurns)
	tracker := completionTracker{budget: budget}
	var finalResponse core.LLMResponse
	var finalStatus core.CompletionStatus
	allResults := make([]core.ActionResult, 0)
	isMinimalContext := true
	consecutiveFailures := 0
	skipTurnStartNarration := false
	for turn := 0; turn < budget.Hard; turn++ {
		narrator := newTurnNarrator(time.Now().UnixNano()+int64(turn), nil)
		if !skipTurnStartNarration {
			e.narrate(narrator.turnStarted())
		}
		skipTurnStartNarration = false
		e.Renderer.BeginThinking(e.thinkingLabelWithPhase("Understanding the task"))
		request := provider.Request{
			Model:       e.Runtime.Model,
			System:      provider.BuildSystemPrompt(e.Runtime.Config.Context.MaxActions, e.Runtime.Interaction),
			Messages:    history,
			Temperature: 0.1,
		}
		response, err := e.Provider.Generate(ctx, request)
		e.Renderer.EndThinking()
		if err != nil {
			return err
		}
		response = normalizedResponse(response)
		record.RequestCount++
		record.TokenUsage = record.TokenUsage.Add(usageOrEstimate(request, response))
		narrator = newTurnNarrator(time.Now().UnixNano()+int64(turn), response.Narration)
		e.narrate(narrator.intent(response))

		if isMinimalContext && shouldCollectFullContextBeforeActions(response, e.Runtime.Interaction) {
			if !e.Runtime.JSONOutput {
				e.Renderer.Message("Inspecting context and reframing plan...")
			}
			e.narrate(narrator.contextShift())
			fullSnapshot, err := e.Collector.Collect(ctx, e.Runtime, stdin)
			if err != nil {
				return err
			}
			isMinimalContext = false
			record.ContextJSON = fullSnapshot.JSON()
			skipTurnStartNarration = true

			// Replace initial user message with full context and re-run the generation
			history[len(history)-1].Content = buildInitialPrompt(prompt, fullSnapshot, graphDigest, opsDigest)
			continue
		}

		record.Findings = append(record.Findings, response.Findings...)
		record.Actions = append(record.Actions, response.Actions...)
		record.NeedsUserInput = response.NeedsUserInput
		record.Confidence = response.Confidence
		response = ensureVisibleResponse(response)
		finalResponse = response

		if err := e.Renderer.Response(response); err != nil {
			return err
		}

		results, stop, err := e.executeTurnWithNarrator(ctx, response, narrator)
		if err != nil {
			return err
		}
		safeResults := sanitizeResults(results)
		allResults = append(allResults, safeResults...)
		if hasErrors(results) {
			consecutiveFailures++
		} else if len(results) > 0 {
			consecutiveFailures = 0
		}

		rawResponse, _ := json.Marshal(response)
		history = append(history, core.ConversationMessage{
			Role:    "assistant",
			Content: string(rawResponse),
		})

		if shouldNudgeForExecutableStep(response, results) {
			history = append(history, missingActionContinuationMessage(response))
			continue
		}

		decision := tracker.decideContinuation(response, safeResults, stop, reachedFailureLimit(consecutiveFailures, e.Runtime.Config.Tools.MaxConsecutiveFailures), turn+1)
		if decision.AnnounceExtended {
			e.narrate(narrator.budgetExtended())
		}
		if decision.Continue {
			if decision.Continuation != nil {
				history = append(history, *decision.Continuation)
			}
			continue
		}
		finalStatus = decision.Status
		if decision.AnnounceHardCap {
			e.narrate(narrator.hardCapReached())
		}
		e.narrate(narrator.turnFinished(finalResponse.Summary))
		break
	}

	if finalStatus == "" {
		if tracker.executedActions {
			finalStatus = core.CompletionIncompleteMaxTurns
		} else {
			finalStatus = core.CompletionFinished
		}
	}
	if shouldSynthesizeOutcome(finalStatus, finalResponse, tracker.executedActions) {
		finalResponse = synthesizedClosingResponse(finalStatus, finalResponse, allResults)
		record.Findings = append(record.Findings, finalResponse.Findings...)
		record.Actions = append(record.Actions, finalResponse.Actions...)
		record.NeedsUserInput = finalResponse.NeedsUserInput
		record.Confidence = finalResponse.Confidence
		if err := e.Renderer.Response(finalResponse); err != nil {
			return err
		}
		rawResponse, _ := json.Marshal(finalResponse)
		history = append(history, core.ConversationMessage{
			Role:    "assistant",
			Content: string(rawResponse),
		})
	}

	record.Summary = finalResponse.Summary
	record.Results = allResults
	record.Memories = memory.ExtractSessionMemories(prompt, finalResponse, allResults, 16)
	record.Messages = sanitizeMessages(history)
	record.CompletionStatus = finalStatus
	record.FinishedAt = time.Now().UTC()
	if err := e.Sessions.Save(record); err != nil {
		return err
	}
	if threadState != nil {
		*threadState = e.Threads.AppendTurn(*threadState, prompt, core.LLMResponse{
			Summary:  finalResponse.Summary,
			Findings: record.Findings,
			Actions:  record.Actions,
		}, record.Results, finalStatus, threads.AppendOptions{
			ResultCharLimit: e.Runtime.Config.Thread.MaxResultChars,
			ThreadConfig:    e.Runtime.Config.Thread,
		})
		if err := e.Threads.Save(*threadState); err != nil {
			return err
		}
		e.warnContextUsage(*threadState, threadRef.Source)
	}
	return nil
}

func (e *Engine) RunChat(ctx context.Context, initialPrompt string, stdin []byte) error {
	if e.Provider == nil {
		return fmt.Errorf("provider is not configured or API key is unavailable")
	}

	record := sessions.Record{
		ID:           newRecordID(),
		Mode:         "chat",
		Provider:     e.Runtime.ProviderName,
		Model:        e.Runtime.Model,
		ApprovalMode: e.Runtime.ApprovalMode,
		StartedAt:    time.Now().UTC(),
	}
	threadRef, threadState, err := e.resolveThreadContext()
	if err != nil {
		return err
	}

	var history []core.ConversationMessage
	if initialPrompt != "" {
		if err := e.runChatPrompt(ctx, initialPrompt, stdin, &history, &record, threadState, threadRef); err != nil {
			if errors.Is(err, errTurnStopped) {
				e.Renderer.Message("Stopped current turn.")
			} else {
				e.Renderer.Message(err.Error())
			}
		}
	}

	lastOpsSummary := ""
	for {
		currentOpsSummary := e.ensureOpsDigest()
		e.Renderer.SetOpsSummary(currentOpsSummary)
		if strings.TrimSpace(currentOpsSummary) != "" && lastOpsSummary != "" && currentOpsSummary != lastOpsSummary {
			e.Renderer.Message("Background ops update: " + currentOpsSummary)
		}
		lastOpsSummary = currentOpsSummary
		promptRaw, err := e.Renderer.ReadPrompt("> ")
		if err != nil {
			if isChatInputClosedError(err) {
				record.FinishedAt = time.Now().UTC()
				return e.Sessions.Save(record)
			}
			return err
		}

		prompt := strings.TrimSpace(promptRaw)

		// Handle Slash Commands
		if strings.HasPrefix(prompt, "/") {
			parts := strings.Fields(prompt)
			cmd := parts[0]
			args := parts[1:]

			switch cmd {
			case "/mode":
				if len(args) > 0 {
					mode := core.InteractionMode(args[0])
					if !mode.Valid() {
						e.Renderer.Message(fmt.Sprintf("Invalid mode: %s. Use execute-first or plan-first.", args[0]))
						continue
					}
					e.Runtime.Interaction = mode
					e.Renderer.SetInteraction(mode)
					e.Renderer.Message(fmt.Sprintf("Interaction mode set to: %s", e.Runtime.Interaction))
				} else {
					e.Renderer.Message(fmt.Sprintf("Current interaction mode: %s. Use /mode <execute-first|plan-first> to change.", e.Runtime.Interaction))
				}
				continue
			case "/approval":
				if len(args) > 0 {
					mode := core.ApprovalMode(args[0])
					if !mode.Valid() {
						e.Renderer.Message(fmt.Sprintf("Invalid approval mode: %s. Use confirm, auto-safe, agent, or suggest.", args[0]))
						continue
					}
					e.Runtime.ApprovalMode = mode
					e.Renderer.SetApproval(mode)
					e.Renderer.Message(fmt.Sprintf("Approval mode set to: %s", e.Runtime.ApprovalMode))
				} else {
					e.Renderer.Message(fmt.Sprintf("Current approval mode: %s. Use /approval <confirm|auto-safe|agent|suggest> to change.", e.Runtime.ApprovalMode))
				}
				continue
			case "/secret":
				if len(args) > 0 {
					switch args[0] {
					case "visible":
						e.Renderer.SetSecretVisibility(true)
						e.Renderer.Message("Secret input visibility set to: visible")
					case "hidden":
						e.Renderer.SetSecretVisibility(false)
						e.Renderer.Message("Secret input visibility set to: hidden")
					default:
						e.Renderer.Message(fmt.Sprintf("Invalid secret visibility: %s. Use visible or hidden.", args[0]))
					}
				} else {
					e.Renderer.Message(fmt.Sprintf("Current secret input visibility: %s. Use /secret <visible|hidden> to change.", e.Renderer.SecretVisibility()))
				}
				continue
			case "/model":
				if len(args) > 0 {
					e.Runtime.Model = args[0]
					e.Executor.ProviderModel = e.Runtime.Model
					e.Renderer.SetModelContext(
						e.Runtime.ProviderName,
						e.Runtime.Model,
						models.SuggestedForProvider(e.Runtime.Config, e.Runtime.ProviderName),
					)
					e.Renderer.Message(fmt.Sprintf("Model set to: %s", e.Runtime.Model))
				} else {
					e.Renderer.Message(models.FormatCurrent(
						e.Runtime.Config,
						e.Runtime.ProviderName,
						e.Runtime.Model,
					))
				}
				continue
			case "/provider":
				if len(args) > 0 {
					e.Runtime.ProviderName = args[0]

					// Re-initialize provider
					if providerCfg, err := e.Runtime.ProviderConfig(); err == nil {
						if apiKey, keyErr := e.Runtime.APIKey(); keyErr == nil {
							if providerCfg.Type == "openai-compatible" {
								if e.Runtime.ProviderName == "openai" {
									e.Provider = provider.OpenAIResponsesFactory{}.New(providerCfg.BaseURL, apiKey, providerCfg.Headers)
								} else {
									e.Provider = provider.OpenAICompatibleFactory{}.New(providerCfg.BaseURL, apiKey, providerCfg.Headers)
								}
								e.Executor.Provider = e.Provider
								e.Executor.ProviderModel = e.Runtime.Model
								e.Renderer.SetModelContext(
									e.Runtime.ProviderName,
									e.Runtime.Model,
									models.SuggestedForProvider(e.Runtime.Config, e.Runtime.ProviderName),
								)
								e.Renderer.Message(fmt.Sprintf("Provider set to: %s", e.Runtime.ProviderName))
							} else {
								e.Renderer.Message(fmt.Sprintf("Provider %s uses an unsupported type: %s", e.Runtime.ProviderName, providerCfg.Type))
							}
						} else {
							e.Renderer.Message(fmt.Sprintf("Failed to get API key for provider: %v", keyErr))
						}
					} else {
						e.Renderer.Message(fmt.Sprintf("Provider error: %v", err))
					}
				} else {
					e.Renderer.Message(fmt.Sprintf("Current provider: %s. Use /provider <name> to change.", e.Runtime.ProviderName))
				}
				continue
			case "/clear":
				history = nil
				e.Renderer.ClearScreen()
				e.Renderer.Message("Chat history cleared. Starting fresh.")
				continue
			case "/ops":
				if e.Ops == nil {
					e.Renderer.Message("Operational memory is unavailable.")
					continue
				}
				summary := e.ensureOpsDigest()
				if strings.TrimSpace(summary) == "" {
					summary = "ops: idle"
				}
				result, err := e.Ops.Query("", time.Now().UTC().Add(-24*time.Hour), 8)
				if err != nil {
					e.Renderer.Message(err.Error())
					continue
				}
				data, _ := json.MarshalIndent(map[string]any{
					"summary": summary,
					"ops":     result,
				}, "", "  ")
				e.Renderer.Message(string(data))
				continue
			case "/watch":
				if e.Ops == nil {
					e.Renderer.Message("Operational runtime is unavailable.")
					continue
				}
				if len(args) == 0 {
					e.Renderer.Message("Usage: /watch <query>")
					continue
				}
				executable, err := os.Executable()
				if err != nil {
					e.Renderer.Message(err.Error())
					continue
				}
				watcher, pid, err := e.Ops.StartWatcher(ctx, e.opsRuntimeDeps(), strings.Join(args, " "), executable)
				if err != nil {
					e.Renderer.Message(err.Error())
					continue
				}
				e.Renderer.Message(fmt.Sprintf("Started watcher %s for %s (pid %d)", watcher.ID, watcher.Entity.DisplayName, pid))
				e.Renderer.SetOpsSummary(e.ensureOpsDigest())
				continue
			case "/recover":
				if e.Ops == nil {
					e.Renderer.Message("Operational runtime is unavailable.")
					continue
				}
				if len(args) == 0 {
					e.Renderer.Message("Usage: /recover <goal>")
					continue
				}
				executable, err := os.Executable()
				if err != nil {
					e.Renderer.Message(err.Error())
					continue
				}
				spec, pid, err := e.Ops.StartRecovery(ctx, e.opsRuntimeDeps(), strings.Join(args, " "), executable)
				if err != nil {
					e.Renderer.Message(err.Error())
					continue
				}
				e.Renderer.Message(fmt.Sprintf("Started recovery %s for %s (pid %d)", spec.ID, spec.Entity.DisplayName, pid))
				e.Renderer.SetOpsSummary(e.ensureOpsDigest())
				continue
			case "/exit":
				record.FinishedAt = time.Now().UTC()
				return e.Sessions.Save(record)
			case "/help":
				e.Renderer.Message("Available slash commands:\n  /mode [name]      - Get or set execute-first / plan-first\n  /approval [name]  - Get or set confirm / auto-safe / agent / suggest\n  /secret [state]   - Get or set secret input visibility\n  /model [name]     - Get or set the current model\n  /provider [name]  - Get or set the current provider\n  /ops              - Show watcher and recovery activity\n  /watch <query>    - Start a background watcher\n  /recover <goal>   - Start a background recovery run\n  /clear            - Clear the conversation history\n  /help             - Show this menu\n  /exit             - Exit the agent session")
				continue
			default:
				e.Renderer.Message(fmt.Sprintf("Unknown command: %s. Type /help for a list of commands.", cmd))
				continue
			}
		}

		switch prompt {
		case "", "help":
			e.Renderer.Message("Type a task, use a slash command like /model, or exit with `exit` or `quit`.")
			continue
		case "exit", "quit":
			record.FinishedAt = time.Now().UTC()
			return e.Sessions.Save(record)
		}
		if err := e.runChatPrompt(ctx, prompt, nil, &history, &record, threadState, threadRef); err != nil {
			if errors.Is(err, errTurnStopped) {
				e.Renderer.Message("Stopped current turn.")
				continue
			}
			e.Renderer.Message(err.Error())
			continue
		}
	}
}

func isChatInputClosedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file already closed") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "use of closed network connection")
}

func shouldCollectFullContextBeforeActions(response core.LLMResponse, interaction core.InteractionMode) bool {
	if len(response.Actions) == 0 {
		return false
	}
	if containsInspectAction(response.Actions) {
		return true
	}
	return interaction == core.InteractionPlanFirst
}

func containsInspectAction(actions []core.Action) bool {
	for _, action := range actions {
		if action.Type == core.ActionInspect {
			return true
		}
	}
	return false
}

func (e *Engine) runChatPrompt(ctx context.Context, prompt string, stdin []byte, history *[]core.ConversationMessage, record *sessions.Record, threadState *threads.Thread, threadRef threads.ThreadRef) error {
	prompt = strings.TrimSpace(prompt)
	if isSimpleGreetingPrompt(prompt) {
		response := core.LLMResponse{Summary: greetingSummary(prompt)}
		record.Prompt += "\n" + prompt
		record.Summary = response.Summary
		if err := e.Renderer.Response(response); err != nil {
			return err
		}
		rawResponse, _ := json.Marshal(response)
		*history = append(*history, core.ConversationMessage{
			Role:    "user",
			Content: prompt,
		}, core.ConversationMessage{
			Role:    "assistant",
			Content: string(rawResponse),
		})
		record.Messages = sanitizeMessages(*history)
		record.FinishedAt = time.Now().UTC()
		return e.Sessions.Save(*record)
	}
	snapshot, err := e.Collector.CollectMinimal(ctx, e.Runtime, stdin)
	if err != nil {
		return err
	}
	minimalSnapshot := snapshot
	graphDigest := e.ensureGraphDigest(ctx, graph.ModeLight)
	opsDigest := e.ensureOpsDigest()
	turnStart := len(*history)
	*history = append(*history, core.ConversationMessage{
		Role:    "user",
		Content: buildInitialPrompt(prompt, snapshot, graphDigest, opsDigest),
	})

	budget := resolveTurnBudget(e.Runtime.Config.Tools.SoftTurns, e.Runtime.Config.Tools.MaxTurns)
	tracker := completionTracker{budget: budget}
	usingFullContext := false
	usingDirectRetry := false

	var finalResponse core.LLMResponse
	allResults := make([]core.ActionResult, 0)
	var finalStatus core.CompletionStatus
	consecutiveFailures := 0

	for turn := 0; turn < budget.Hard; turn++ {
		var response core.LLMResponse
		turnStartNarrated := false
		for attempts := 0; attempts < 4; attempts++ {
			narrator := newTurnNarrator(time.Now().UnixNano()+int64(turn*10+attempts), nil)
			if !turnStartNarrated {
				e.narrate(narrator.turnStarted())
				turnStartNarrated = true
			}
			e.Renderer.BeginThinking(e.thinkingLabelWithPhase("Understanding the task"))
			var stopped bool
			request := provider.Request{
				Model:       e.Runtime.Model,
				System:      provider.BuildSystemPrompt(e.Runtime.Config.Context.MaxActions, e.Runtime.Interaction),
				Messages:    *history,
				Temperature: 0.1,
			}
			response, stopped, err = e.generateResponse(ctx, request)
			e.Renderer.EndThinking()
			if err != nil {
				return err
			}
			if stopped {
				e.narrate(narrator.interrupted())
				return errTurnStopped
			}
			record.RequestCount++
			record.TokenUsage = record.TokenUsage.Add(usageOrEstimate(request, response))
			response = normalizedResponse(response)
			if shouldReplaceWithGreetingSummary(prompt, response) {
				response = greetingOnlyResponse(prompt)
			}
			if !usingFullContext && shouldCollectFullContextBeforeActions(response, e.Runtime.Interaction) {
				fullSnapshot, err := e.Collector.Collect(ctx, e.Runtime, stdin)
				if err != nil {
					return err
				}
				usingFullContext = true
				snapshot = fullSnapshot
				(*history)[len(*history)-1].Content = buildInitialPrompt(prompt, snapshot, graphDigest, opsDigest)
				e.narrate(narrator.contextShift())
				continue
			}
			if !usingDirectRetry && isSyntheticFallbackResponse(response) {
				usingDirectRetry = true
				snapshot = minimalSnapshot
				*history = append((*history)[:turnStart], core.ConversationMessage{
					Role:    "user",
					Content: prompt,
				})
				continue
			}
			narrator = newTurnNarrator(time.Now().UnixNano()+int64(turn*10+attempts), response.Narration)
			e.narrate(narrator.intent(response))
			if turn == 0 && attempts == 0 && shouldNudgeForExecutableStep(response, nil) {
				rawResponse, _ := json.Marshal(response)
				*history = append(*history, core.ConversationMessage{
					Role:    "assistant",
					Content: string(rawResponse),
				})
				*history = append(*history, missingActionContinuationMessage(response))
				continue
			}
			break
		}

		if tracker.executedActions && isSyntheticFallbackResponse(response) {
			finalResponse = core.LLMResponse{}
			break
		}
		response = ensureVisibleResponse(response)
		finalResponse = response
		record.Findings = append(record.Findings, response.Findings...)
		record.Actions = append(record.Actions, response.Actions...)
		record.NeedsUserInput = response.NeedsUserInput
		record.Confidence = response.Confidence
		if err := e.Renderer.Response(response); err != nil {
			return err
		}

		narrator := newTurnNarrator(time.Now().UnixNano()+int64(turn), response.Narration)
		results, stop, err := e.executeTurnWithNarrator(ctx, response, narrator)
		if err != nil {
			return err
		}
		safeResults := sanitizeResults(results)
		allResults = append(allResults, safeResults...)
		rawResponse, _ := json.Marshal(response)
		*history = append(*history, core.ConversationMessage{
			Role:    "assistant",
			Content: string(rawResponse),
		})

		if len(results) > 0 {
			if hasErrors(results) {
				consecutiveFailures++
			} else {
				consecutiveFailures = 0
			}
		}
		decision := tracker.decideContinuation(response, safeResults, stop, reachedFailureLimit(consecutiveFailures, e.Runtime.Config.Tools.MaxConsecutiveFailures), turn+1)
		if decision.AnnounceExtended {
			e.narrate(narrator.budgetExtended())
		}
		if decision.Continue {
			if decision.Continuation != nil {
				*history = append(*history, *decision.Continuation)
			}
			continue
		}
		finalStatus = decision.Status
		if decision.AnnounceHardCap {
			e.narrate(narrator.hardCapReached())
		}
		e.narrate(narrator.turnFinished(response.Summary))
		break
	}

	record.Prompt += "\n" + prompt
	record.ContextJSON = snapshot.JSON()

	if finalStatus == "" {
		if tracker.executedActions {
			finalStatus = core.CompletionIncompleteMaxTurns
		} else {
			finalStatus = core.CompletionFinished
		}
	}

	if shouldSynthesizeOutcome(finalStatus, finalResponse, tracker.executedActions) {
		finalResponse = synthesizedClosingResponse(finalStatus, finalResponse, allResults)
		record.Findings = append(record.Findings, finalResponse.Findings...)
		record.Actions = append(record.Actions, finalResponse.Actions...)
		record.NeedsUserInput = finalResponse.NeedsUserInput
		record.Confidence = finalResponse.Confidence
		if err := e.Renderer.Response(finalResponse); err != nil {
			return err
		}
		rawResponse, _ := json.Marshal(finalResponse)
		*history = append(*history, core.ConversationMessage{
			Role:    "assistant",
			Content: string(rawResponse),
		})
	}

	record.Summary = finalResponse.Summary
	record.Results = append(record.Results, allResults...)
	record.Memories = memory.ExtractSessionMemories(prompt, finalResponse, allResults, 16)
	record.Messages = sanitizeMessages(*history)
	record.CompletionStatus = finalStatus
	record.FinishedAt = time.Now().UTC()
	if err := e.Sessions.Save(*record); err != nil {
		return err
	}
	if threadState != nil {
		*threadState = e.Threads.AppendTurn(*threadState, prompt, finalResponse, allResults, finalStatus, threads.AppendOptions{
			ResultCharLimit: e.Runtime.Config.Thread.MaxResultChars,
			ThreadConfig:    e.Runtime.Config.Thread,
		})
		if err := e.Threads.Save(*threadState); err != nil {
			return err
		}
		e.warnContextUsage(*threadState, threadRef.Source)
	}
	return nil
}

func (e *Engine) generateResponse(ctx context.Context, request provider.Request) (core.LLMResponse, bool, error) {
	req := request
	for attempt := 0; attempt < 2; attempt++ {
		generateCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt)
		defer signal.Stop(signals)

		var stopped atomic.Bool
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-signals:
				stopped.Store(true)
				cancel()
			case <-generateCtx.Done():
			}
		}()

		response, err := e.Provider.Generate(generateCtx, req)
		cancel()
		<-done
		if err != nil {
			if stopped.Load() && errors.Is(err, context.Canceled) {
				return core.LLMResponse{}, true, nil
			}
			return core.LLMResponse{}, false, err
		}
		if responseHasVisibleContent(response) {
			return normalizedResponse(response), false, nil
		}
		rawResponse, _ := json.Marshal(response)
		req.Messages = append(append([]core.ConversationMessage{}, req.Messages...), core.ConversationMessage{
			Role:    "assistant",
			Content: string(rawResponse),
		}, core.ConversationMessage{
			Role:    "user",
			Content: "Your last JSON response had an empty summary and no visible answer. Return the full JSON again with a non-empty summary sentence. If you need a tool step, include it in actions.",
		})
	}
	return normalizedResponse(core.LLMResponse{}), false, nil
}

func ensureVisibleResponse(response core.LLMResponse) core.LLMResponse {
	return normalizedResponse(response)
}

func (e *Engine) executeTurn(ctx context.Context, response core.LLMResponse) ([]core.ActionResult, bool, error) {
	return e.executeTurnWithNarrator(ctx, response, nil)
}

func (e *Engine) executeTurnWithNarrator(ctx context.Context, response core.LLMResponse, narrator *turnNarrator) ([]core.ActionResult, bool, error) {
	if len(response.Actions) == 0 && len(response.Verification) == 0 {
		return nil, true, nil
	}

	allowedAskUser := false
	for _, action := range response.Actions {
		if action.Type == core.ActionAskUser && isAskUserAllowed(action) {
			allowedAskUser = true
			break
		}
	}
	if !allowedAskUser {
		response.NeedsUserInput = false
	}

	previews := make([]core.RiskReport, 0, len(response.Actions))
	resolutions := make([]policy.Resolution, 0, len(response.Actions))
	needApproval := false
	for _, action := range response.Actions {
		report, err := e.Executor.Preview(action, e.Runtime.ReadOnly)
		if err != nil {
			return nil, false, err
		}
		resolution, err := e.PolicyStore.Resolve(action)
		if err != nil {
			return nil, false, err
		}
		previews = append(previews, report)
		resolutions = append(resolutions, resolution)
		if e.actionNeedsApproval(action, report, resolution) {
			needApproval = true
		}
	}
	if e.Runtime.Interaction == core.InteractionPlanFirst {
		e.Renderer.PlannedActions(response.Actions, previews)
	}
	if narrator != nil && len(response.Actions) > 1 {
		e.narrate(narrator.actionGroup(response.Actions))
	}
	if len(response.Verification) > 0 && !e.Runtime.JSONOutput && e.Runtime.Interaction == core.InteractionPlanFirst {
		e.Renderer.Message("\nVerification planned after execution.")
	}

	if e.Runtime.ApprovalMode == core.ApprovalSuggest {
		return nil, true, nil
	}

	strategy := "all"
	if needApproval && e.Runtime.Interaction == core.InteractionPlanFirst {
		choice, err := e.Renderer.PromptChoice()
		if err != nil {
			return nil, false, err
		}
		switch choice {
		case "1":
			strategy = "all"
		case "2":
			strategy = "step"
		case "3", "4":
			return nil, true, nil
		default:
			return nil, true, nil
		}
	}

	results := []core.ActionResult{}
	for idx, action := range response.Actions {
		report := previews[idx]
		resolution := resolutions[idx]
		if action.Type == core.ActionFinish {
			return results, true, nil
		}
		if action.Type == core.ActionInspect {
			continue
		}
		if action.Type == core.ActionAskUser {
			if !isAskUserAllowed(action) {
				result := core.ActionResult{
					Action:   action,
					Risk:     report,
					Skipped:  true,
					Summary:  "ask_user blocked: only secret or manual_wait inputs are allowed",
					Approved: true,
				}
				if !e.Runtime.JSONOutput {
					e.Renderer.Result(result)
				}
				results = append(results, result)
				continue
			}
			if !shouldUseStructuredInput(action) {
				if responseHasVisibleContent(response) {
					return results, true, nil
				}
				e.Renderer.Message("Reply in chat with the clarification and I'll continue.")
				return results, true, nil
			}
			if narrator != nil {
				e.narrate(narrator.blocked(action, firstNonEmpty(action.Prompt, action.Reason, "I need your input before I can continue."), core.NarrativeRunning))
			}
			result, err := e.Renderer.CollectUserInput(action)
			if err != nil {
				return results, false, err
			}
			result.Action = action
			result.Risk = report
			result.Approved = true
			e.Renderer.Result(redactActionResult(result))
			if narrator != nil {
				e.narrate(narrator.actionFinished(result))
			}
			results = append(results, result)
			continue
		}
		if resolution.Match.Matched && resolution.Match.Decision == policy.DecisionDeny {
			if narrator != nil {
				e.narrate(narrator.blocked(action, "Machine policy blocked that step.", core.NarrativeSkipped))
			}
			results = append(results, core.ActionResult{
				Action:   action,
				Risk:     report,
				Skipped:  true,
				Summary:  "blocked by machine policy",
				Approved: false,
			})
			if !e.Runtime.JSONOutput {
				e.Renderer.Result(results[len(results)-1])
			}
			continue
		}

		result, stop, err := e.executeAction(ctx, action, report, resolution, strategy, e.Runtime.ReadOnly, e.Runtime.Interaction == core.InteractionExecuteFirst, narrator)
		if !stop {
			results = append(results, result)
		}
		if err != nil {
			if isEditPatchFailure(result) {
				if followUp := e.autoReadAfterPatchFailure(ctx, result.Action, narrator); followUp != nil {
					results = append(results, *followUp)
				}
			}
			return results, false, nil
		}
		if stop {
			return results, true, nil
		}
	}

	if len(response.Verification) > 0 {
		for _, verify := range response.Verification {
			action := core.Action{
				ID:         "verify",
				Type:       verify.Type,
				Title:      "Verification",
				Command:    verify.Command,
				Path:       verify.Path,
				Paths:      verify.Paths,
				Reason:     verify.SuccessHint,
				TimeoutSec: verify.TimeoutSec,
			}
			report, err := e.Executor.Preview(action, e.Runtime.ReadOnly)
			if err != nil {
				return results, false, err
			}
			resolution, err := e.PolicyStore.Resolve(action)
			if err != nil {
				return results, false, err
			}
			if narrator != nil {
				e.narrate(narrator.verificationStarted(action))
			}
			result, stop, err := e.executeAction(ctx, action, report, resolution, strategy, e.Runtime.ReadOnly, false, narrator)
			if !stop {
				results = append(results, result)
			}
			if err != nil {
				return results, false, nil
			}
			if stop {
				return results, true, nil
			}
		}
	}

	return results, false, nil
}

func isEditPatchFailure(result core.ActionResult) bool {
	return result.Action.Type == core.ActionEditFile && strings.TrimSpace(result.Summary) == "the generated edit patch was invalid"
}

func (e *Engine) autoReadAfterPatchFailure(ctx context.Context, action core.Action, narrator *turnNarrator) *core.ActionResult {
	target := firstNonEmpty(action.Path, firstPath(action.Paths))
	if strings.TrimSpace(target) == "" {
		return nil
	}
	readAction := core.Action{
		ID:     action.ID + "-auto-read",
		Type:   core.ActionReadFiles,
		Title:  "Read file after patch failure",
		Reason: "Capture current file contents to repair the edit.",
		Path:   target,
	}
	report, err := e.Executor.Preview(readAction, e.Runtime.ReadOnly)
	if err != nil {
		result := core.ActionResult{Action: readAction, Risk: report, Skipped: true, Summary: err.Error()}
		return &result
	}
	resolution, err := e.PolicyStore.Resolve(readAction)
	if err != nil {
		result := core.ActionResult{Action: readAction, Risk: report, Skipped: true, Summary: err.Error()}
		return &result
	}
	if resolution.Match.Matched && resolution.Match.Decision == policy.DecisionDeny {
		result := core.ActionResult{Action: readAction, Risk: report, Skipped: true, Summary: "auto-read blocked by machine policy"}
		return &result
	}
	if narrator != nil {
		e.narrate(narrator.actionStarted(readAction))
	}
	result, err := e.Executor.Execute(ctx, readAction, e.Runtime.ReadOnly)
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	if !e.Runtime.JSONOutput {
		e.Renderer.Result(result)
	}
	if narrator != nil {
		e.narrate(narrator.actionFinished(result))
	}
	return &result
}

func shouldUseStructuredInput(action core.Action) bool {
	switch action.InputKind {
	case core.InputSecret, core.InputConfirm, core.InputManualWait:
		return true
	case core.InputText:
		if action.ExpectsValue && strings.TrimSpace(action.DestinationHint) != "" {
			return true
		}
		joined := strings.ToLower(strings.Join([]string{
			action.FieldKey,
			action.Prompt,
			action.Reason,
			action.Placeholder,
			action.DestinationHint,
		}, " "))
		joined = strings.NewReplacer("_", " ", "-", " ").Replace(joined)
		for _, marker := range []string{
			"token",
			"api key",
			"apikey",
			"secret",
			"password",
			"passphrase",
			"service name",
			"hostname",
			"host name",
			"branch name",
			"port",
			"url",
		} {
			if strings.Contains(joined, marker) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func isAskUserAllowed(action core.Action) bool {
	switch action.InputKind {
	case core.InputSecret, core.InputManualWait:
		return true
	default:
		return false
	}
}

func (e *Engine) executeAction(ctx context.Context, action core.Action, report core.RiskReport, resolution policy.Resolution, strategy string, readOnly bool, showProgress bool, narrator *turnNarrator) (core.ActionResult, bool, error) {
	if resolution.Match.Matched && resolution.Match.Decision == policy.DecisionDeny {
		result := core.ActionResult{
			Action:   action,
			Risk:     report,
			Skipped:  true,
			Summary:  "blocked by machine policy",
			Approved: false,
		}
		if !e.Runtime.JSONOutput {
			e.Renderer.Result(result)
		}
		if narrator != nil {
			e.narrate(narrator.blocked(action, "Machine policy blocked that step.", core.NarrativeSkipped))
		}
		return result, false, nil
	}

	approved := true
	if e.actionNeedsApproval(action, report, resolution) {
		decision, err := e.promptForActionDecision(action, report, strategy)
		if err != nil {
			return core.ActionResult{}, false, err
		}
		switch decision.Kind {
		case core.ApprovalDecisionAllowOnce:
			approved = true
		case core.ApprovalDecisionAllowAlways:
			approved = true
			if _, err := e.PolicyStore.Add(policy.RuleForAction(action, policy.DecisionAllow)); err != nil {
				return core.ActionResult{}, false, err
			}
		case core.ApprovalDecisionAutoAccept:
			approved = true
			if err := e.PolicyStore.SetAutoAcceptThrough(decision.AutoAcceptThrough); err != nil {
				return core.ActionResult{}, false, err
			}
		case core.ApprovalDecisionDenyOnce:
			approved = false
		case core.ApprovalDecisionCancel:
			return core.ActionResult{}, true, nil
		default:
			approved = false
		}
	}
	if !approved {
		result := core.ActionResult{
			Action:   action,
			Risk:     report,
			Skipped:  true,
			Summary:  "user declined action",
			Approved: false,
		}
		if !e.Runtime.JSONOutput {
			e.Renderer.Result(result)
		}
		if narrator != nil {
			e.narrate(narrator.blocked(action, "That action was skipped because approval was denied.", core.NarrativeSkipped))
		}
		return result, false, nil
	}
	if narrator != nil {
		e.narrate(narrator.actionStarted(action))
	}
	if action.Type == core.ActionRunShell {
		if promoted, ok, err := e.maybePromoteShellAction(ctx, action, report); ok || err != nil {
			if err == nil {
				e.Renderer.Result(promoted)
				if narrator != nil {
					e.narrate(narrator.actionFinished(promoted))
				}
			}
			return promoted, false, err
		}
	}
	if showProgress && !e.Runtime.JSONOutput {
		e.Renderer.ActionProgress(action)
	}
	execFn := e.Executor.Execute
	if showProgress && !e.Runtime.JSONOutput && action.Type == core.ActionRunShell {
		execFn = func(ctx context.Context, action core.Action, readOnly bool) (core.ActionResult, error) {
			return e.Executor.ExecuteStream(ctx, action, readOnly, func(chunk core.ActionOutputChunk) {
				e.Renderer.StreamActionOutput(action, chunk)
			})
		}
	}
	result, err := execFn(ctx, action, readOnly)
	if err != nil && action.Type == core.ActionRunShell && result.BackgroundRecommended {
		if promoted, ok, promoteErr := e.promoteShellFailure(ctx, action, result); ok || promoteErr != nil {
			if promoteErr == nil {
				result = promoted
				err = nil
			} else {
				err = promoteErr
			}
		}
	}
	e.Renderer.Result(result)
	if narrator != nil {
		e.narrate(narrator.actionFinished(result))
	}
	return result, false, err
}

func (e *Engine) actionNeedsApproval(action core.Action, report core.RiskReport, resolution policy.Resolution) bool {
	if resolution.Match.Matched && resolution.Match.Decision == policy.DecisionAllow && report.Level != core.RiskHigh {
		return false
	}
	if resolution.AutoAcceptThrough.Covers(report.Level) {
		return false
	}
	if e.Executor.Classifier.IsSensitiveAction(action) {
		return true
	}
	return e.Executor.Classifier.NeedsApproval(action, report, e.Runtime.ApprovalMode, e.Runtime.Config.Security.AutoApproveReadTools, e.Runtime.ReadOnly)
}

func (e *Engine) promptForActionDecision(action core.Action, report core.RiskReport, strategy string) (core.ApprovalDecision, error) {
	if e.Runtime.Interaction == core.InteractionPlanFirst && strategy == "all" && !e.Executor.Classifier.RequiresDoubleConfirm(report) {
		return core.ApprovalDecision{Kind: core.ApprovalDecisionAllowOnce}, nil
	}
	if e.Runtime.Interaction == core.InteractionPlanFirst && strategy != "step" && e.Executor.Classifier.RequiresDoubleConfirm(report) {
		ok, err := e.Renderer.Confirm(action.Title, true)
		if err != nil {
			return core.ApprovalDecision{}, err
		}
		if ok {
			return core.ApprovalDecision{Kind: core.ApprovalDecisionAllowOnce}, nil
		}
		return core.ApprovalDecision{Kind: core.ApprovalDecisionDenyOnce}, nil
	}
	e.Renderer.ApprovalPrompt(action, report)
	choice, err := e.Renderer.PromptApprovalChoice(report.Level)
	if err != nil {
		return core.ApprovalDecision{}, err
	}
	return choice, nil
}

func hasErrors(results []core.ActionResult) bool {
	for _, result := range results {
		if result.BackgroundRunID != "" {
			continue
		}
		if result.Error != "" {
			return true
		}
	}
	return false
}

func reachedFailureLimit(current, limit int) bool {
	if limit <= 0 {
		limit = 2
	}
	return current >= limit
}

func resolveTurnBudget(soft, hard int) turnBudget {
	if hard <= 0 {
		hard = 12
	}
	if soft <= 0 {
		soft = 5
	}
	if soft > hard {
		soft = hard
	}
	return turnBudget{Soft: soft, Hard: hard}
}

func (t *completionTracker) decideContinuation(response core.LLMResponse, results []core.ActionResult, stop, failureLimitHit bool, turnUsed int) continuationDecision {
	finishSeen := responseHasFinishAction(response)
	if finishSeen {
		return continuationDecision{Status: core.CompletionFinished}
	}
	if blockedByUserInput(response, results, stop) {
		return continuationDecision{Status: core.CompletionBlockedUserInput}
	}
	if hasBackgroundRun(results) {
		return continuationDecision{Status: core.CompletionBackgroundContinuing}
	}
	if failureLimitHit {
		return continuationDecision{Status: core.CompletionFailed}
	}
	if turnUsed >= t.budget.Hard {
		if t.executedActions {
			return continuationDecision{Status: core.CompletionIncompleteMaxTurns, AnnounceHardCap: true}
		}
		return continuationDecision{Status: core.CompletionFinished}
	}

	actionKey, resultKey, stage, executedTool := fingerprintExecutedResults(results)
	if executedTool {
		progress := !t.executedActions || actionKey != t.lastActionKey || resultKey != t.lastResultKey || stage != t.lastStage
		t.executedActions = true
		if turnUsed >= t.budget.Soft {
			if progress {
				t.noProgressStreak = 0
			} else {
				t.noProgressStreak++
			}
		} else {
			t.noProgressStreak = 0
		}
		t.lastActionKey = actionKey
		t.lastResultKey = resultKey
		t.lastReplyKey = ""
		t.lastStage = stage
		if turnUsed >= t.budget.Soft && t.noProgressStreak >= 2 {
			return continuationDecision{Status: core.CompletionIncompleteNoProgress}
		}
		msg := buildContinuationMessage(results)
		decision := continuationDecision{
			Continue:     true,
			Continuation: &msg,
		}
		if turnUsed >= t.budget.Soft && !t.extensionNarrated {
			decision.AnnounceExtended = true
			t.extensionNarrated = true
		}
		return decision
	}

	if len(results) > 0 {
		msg := buildContinuationMessage(results)
		return continuationDecision{
			Continue:     true,
			Continuation: &msg,
		}
	}

	if t.executedActions && len(response.Actions) == 0 && len(response.Verification) == 0 && !response.NeedsUserInput {
		replyKey := strings.TrimSpace(response.Summary) + "|" + strings.Join(response.Findings, "|")
		if turnUsed >= t.budget.Soft {
			if replyKey == t.lastReplyKey {
				t.noProgressStreak++
			} else {
				t.noProgressStreak = 0
			}
		}
		t.lastReplyKey = replyKey
		if turnUsed >= t.budget.Soft && t.noProgressStreak >= 2 {
			return continuationDecision{Status: core.CompletionIncompleteNoProgress}
		}
		msg := explicitFinishContinuationMessage(response)
		decision := continuationDecision{
			Continue:     true,
			Continuation: &msg,
		}
		if turnUsed >= t.budget.Soft && !t.extensionNarrated {
			decision.AnnounceExtended = true
			t.extensionNarrated = true
		}
		return decision
	}

	if !t.executedActions && (stop || (len(response.Actions) == 0 && len(response.Verification) == 0)) {
		return continuationDecision{Status: core.CompletionFinished}
	}

	if len(response.Actions) > 0 || len(response.Verification) > 0 {
		msg := missingActionContinuationMessage(response)
		return continuationDecision{
			Continue:     true,
			Continuation: &msg,
		}
	}

	return continuationDecision{Status: core.CompletionFinished}
}

func buildContinuationMessage(results []core.ActionResult) core.ConversationMessage {
	if followUp := findFollowUp(results); followUp != nil {
		return core.ConversationMessage{
			Role: "user",
			Content: fmt.Sprintf("User follow-up while blocked:\nfield=%s\nkind=%s\nmessage=%s\nContinue the task using this clarification and only ask for another blocker if still required.",
				followUp.FieldKey,
				followUp.InputKind,
				followUp.InputValue,
			),
		}
	}
	if runID, summary, ok := findBackgroundRunContinuation(results); ok {
		return core.ConversationMessage{
			Role: "user",
			Content: fmt.Sprintf("Background run status:\nrun_id=%s\nstate=running\nsummary=%s\nThe durable shell run is still active. Do not verify completion-dependent outcomes yet. Wait, inspect logs, or explain that the task is continuing in background until this run succeeds or fails.",
				runID,
				summary,
			),
		}
	}
	resultsJSON, _ := json.Marshal(results)
	return core.ConversationMessage{
		Role: "user",
		Content: "Action results:\n" + string(resultsJSON) +
			"\nReturn a finish action only if the task is complete. Otherwise return the next smallest safe action.",
	}
}

func synthesizedClosingResponse(status core.CompletionStatus, response core.LLMResponse, results []core.ActionResult) core.LLMResponse {
	latest := latestUsefulResult(results)
	latestText := "no additional result details were captured"
	if latest != nil {
		latestText = firstNonEmpty(latest.Summary, latest.Error, latest.Action.Reason, latest.Action.Title, latest.Action.Command, latest.Action.Query, latest.Action.Path)
	}
	nextStep := nextRequiredStepHint(response, results)
	switch status {
	case core.CompletionFinished:
		return core.LLMResponse{
			Summary: fmt.Sprintf("I completed the task. Latest confirmed step: %s.", latestText),
		}
	case core.CompletionIncompleteMaxTurns:
		return core.LLMResponse{
			Summary: fmt.Sprintf("I stopped before completion after reaching the hard turn cap. Latest useful result: %s. Next required step: %s.", latestText, nextStep),
		}
	case core.CompletionIncompleteNoProgress:
		return core.LLMResponse{
			Summary: fmt.Sprintf("I stopped before completion because the last turns were repeating without new progress. Latest useful result: %s. Next required step: %s.", latestText, nextStep),
		}
	case core.CompletionBlockedUserInput:
		return core.LLMResponse{
			Summary: fmt.Sprintf("I’m blocked on required user input before the task can complete. Next required step: %s.", nextStep),
		}
	case core.CompletionFailed:
		return core.LLMResponse{
			Summary: fmt.Sprintf("I stopped before completion because repeated action failures prevented further progress. Latest failure: %s. Next required step: %s.", latestText, nextStep),
		}
	case core.CompletionBackgroundContinuing:
		if latest != nil && latest.BackgroundRunID != "" {
			return core.LLMResponse{
				Summary: fmt.Sprintf("I moved the remaining work into durable background run %s, so the task is continuing outside this turn.", latest.BackgroundRunID),
			}
		}
		return core.LLMResponse{
			Summary: "The task is continuing outside this turn in background work.",
		}
	default:
		return core.LLMResponse{
			Summary: fmt.Sprintf("I stopped before completion. Latest useful result: %s. Next required step: %s.", latestText, nextStep),
		}
	}
}

func (e *Engine) maybePromoteShellAction(ctx context.Context, action core.Action, report core.RiskReport) (core.ActionResult, bool, error) {
	if !e.shouldPromoteShellBeforeRun(action) {
		return core.ActionResult{}, false, nil
	}
	return e.startBackgroundShellRun(ctx, action, report, ops.ShellPromotionLongExpected)
}

func (e *Engine) promoteShellFailure(ctx context.Context, action core.Action, failed core.ActionResult) (core.ActionResult, bool, error) {
	if !action.Detach {
		return core.ActionResult{}, false, nil
	}
	var reason ops.ShellPromotionReason
	switch failed.FailureKind {
	case core.ShellFailureTimeout:
		reason = ops.ShellPromotionTimedOut
	case core.ShellFailureSignalKilled:
		reason = ops.ShellPromotionSignalKilled
	case core.ShellFailureStalled:
		reason = ops.ShellPromotionStalled
	default:
		return core.ActionResult{}, false, nil
	}
	return e.startBackgroundShellRun(ctx, action, failed.Risk, reason)
}

func (e *Engine) startBackgroundShellRun(ctx context.Context, action core.Action, report core.RiskReport, reason ops.ShellPromotionReason) (core.ActionResult, bool, error) {
	if e.Ops == nil {
		return core.ActionResult{}, false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return core.ActionResult{}, false, err
	}
	status, pid, err := e.Ops.StartShellRun(ctx, e.opsRuntimeDeps(), action, reason, executable)
	if err != nil {
		return core.ActionResult{}, false, err
	}
	summary := fmt.Sprintf("continuing in background run %s (pid %d)", status.Spec.ID, pid)
	if action.Detach {
		summary = fmt.Sprintf("started background run %s (pid %d)", status.Spec.ID, pid)
	}
	result := core.ActionResult{
		Action:                action,
		Risk:                  report,
		Approved:              true,
		BackgroundRunID:       status.Spec.ID,
		BackgroundRecommended: true,
		Retryable:             true,
		Summary:               summary,
	}
	return result, true, nil
}

func (e *Engine) shouldPromoteShellBeforeRun(action core.Action) bool {
	if e.Ops == nil || action.Type != core.ActionRunShell {
		return false
	}
	return action.Detach
}

func shouldNudgeForExecutableStep(response core.LLMResponse, results []core.ActionResult) bool {
	if len(results) > 0 || len(response.Actions) > 0 || len(response.Verification) > 0 || response.NeedsUserInput {
		return false
	}
	if response.Confidence >= 0.85 {
		return false
	}
	summary := strings.ToLower(strings.TrimSpace(response.Summary))
	if summary == "" {
		return false
	}
	intentPhrases := []string{
		"will ",
		"going to ",
		"proceed",
		"inspect",
		"check",
		"look for",
		"list ",
		"verify",
		"search",
		"read ",
		"run ",
	}
	for _, phrase := range intentPhrases {
		if strings.Contains(summary, phrase) {
			return true
		}
	}
	for _, finding := range response.Findings {
		line := strings.ToLower(finding)
		for _, phrase := range intentPhrases {
			if strings.Contains(line, phrase) {
				return true
			}
		}
	}
	return false
}

func missingActionContinuationMessage(response core.LLMResponse) core.ConversationMessage {
	return core.ConversationMessage{
		Role: "user",
		Content: fmt.Sprintf("Your last response described a next step but returned no executable actions or verification.\nSummary: %s\nDo not answer with vague prose. Return the smallest concrete next action now, or return a finish action if the task is already complete.",
			response.Summary),
	}
}

func explicitFinishContinuationMessage(response core.LLMResponse) core.ConversationMessage {
	return core.ConversationMessage{
		Role: "user",
		Content: fmt.Sprintf("You already executed actions on this task.\nLatest summary: %s\nDo not stop with prose only. Return a finish action if the task is complete, or return the next smallest safe action if it is not.",
			response.Summary),
	}
}

func shouldSynthesizeOutcome(status core.CompletionStatus, response core.LLMResponse, executedActions bool) bool {
	if status != core.CompletionFinished {
		if status == core.CompletionBlockedUserInput && responseHasVisibleContent(response) {
			return false
		}
		return true
	}
	if !executedActions {
		return false
	}
	if isSyntheticFallbackResponse(response) {
		return true
	}
	return (len(response.Actions) > 0 && !responseHasFinishAction(response)) || len(response.Verification) > 0 || responseHasAllowedAskUserAction(response)
}

func responseHasFinishAction(response core.LLMResponse) bool {
	for _, action := range response.Actions {
		if action.Type == core.ActionFinish {
			return true
		}
	}
	return false
}

func responseHasAskUserAction(response core.LLMResponse) bool {
	for _, action := range response.Actions {
		if action.Type == core.ActionAskUser {
			return true
		}
	}
	return false
}

func responseHasAllowedAskUserAction(response core.LLMResponse) bool {
	for _, action := range response.Actions {
		if action.Type == core.ActionAskUser && isAskUserAllowed(action) {
			return true
		}
	}
	return false
}

func blockedByUserInput(response core.LLMResponse, results []core.ActionResult, stop bool) bool {
	if !stop {
		return false
	}
	inputSeen := false
	for _, result := range results {
		if result.InputKind != "" && result.ResponseMode != core.InputResponseSubmitted && result.ResponseMode != core.InputResponseFollowUp {
			if result.ResponseMode == core.InputResponseSkipped {
				inputSeen = true
				continue
			}
			return true
		}
		if result.InputKind != "" {
			inputSeen = true
		}
	}
	if response.NeedsUserInput || responseHasAllowedAskUserAction(response) {
		return !inputSeen
	}
	return false
}

func hasBackgroundRun(results []core.ActionResult) bool {
	for _, result := range results {
		if strings.TrimSpace(result.BackgroundRunID) != "" && !result.Action.Detach {
			return true
		}
	}
	return false
}

func fingerprintExecutedResults(results []core.ActionResult) (string, string, string, bool) {
	actionParts := []string{}
	resultParts := []string{}
	stage := ""
	for _, result := range results {
		if result.Skipped || result.Action.Type == core.ActionAskUser || result.Action.Type == core.ActionFinish {
			continue
		}
		if stage == "" {
			stage = string(result.Action.Type)
		}
		actionTarget := firstNonEmpty(result.Action.Path, firstPath(result.Action.Paths), result.Action.Query, result.Action.Pattern, result.Action.Command, result.Action.Title)
		actionParts = append(actionParts, string(result.Action.Type)+"|"+actionTarget)
		resultParts = append(resultParts, firstNonEmpty(result.Summary, result.Error, result.Stdout, result.Stderr))
	}
	if len(actionParts) == 0 {
		return "", "", "", false
	}
	return strings.Join(actionParts, "||"), strings.Join(resultParts, "||"), stage, true
}

func latestUsefulResult(results []core.ActionResult) *core.ActionResult {
	for i := len(results) - 1; i >= 0; i-- {
		result := &results[i]
		if strings.TrimSpace(result.Summary) != "" || strings.TrimSpace(result.Error) != "" || strings.TrimSpace(result.BackgroundRunID) != "" {
			return result
		}
	}
	return nil
}

func nextRequiredStepHint(response core.LLMResponse, results []core.ActionResult) string {
	for _, action := range response.Actions {
		if action.Type == core.ActionFinish {
			continue
		}
		return firstNonEmpty(action.Reason, action.Title, action.Query, action.Pattern, action.Command, action.Path, action.FieldKey)
	}
	for _, verify := range response.Verification {
		return firstNonEmpty(verify.SuccessHint, verify.Command, verify.Path, firstPath(verify.Paths))
	}
	if latest := latestUsefulResult(results); latest != nil {
		if latest.Action.Type == core.ActionAskUser {
			return firstNonEmpty(latest.Action.Prompt, latest.Action.Reason, latest.Action.FieldKey, "provide the required input")
		}
		return firstNonEmpty(latest.Action.Reason, latest.Action.Title, latest.Action.Query, latest.Action.Pattern, latest.Action.Command, latest.Action.Path, "take the next smallest safe action")
	}
	if strings.TrimSpace(response.Summary) != "" {
		return strings.TrimSpace(response.Summary)
	}
	return "take the next smallest safe action"
}

func findFollowUp(results []core.ActionResult) *core.ActionResult {
	for i := range results {
		if results[i].ResponseMode == core.InputResponseFollowUp {
			return &results[i]
		}
	}
	return nil
}

func findBackgroundRunContinuation(results []core.ActionResult) (string, string, bool) {
	for _, result := range results {
		if strings.TrimSpace(result.BackgroundRunID) == "" || result.Action.Detach {
			continue
		}
		return result.BackgroundRunID, firstNonEmpty(result.Summary, "background work is continuing"), true
	}
	return "", "", false
}

func sanitizeResults(results []core.ActionResult) []core.ActionResult {
	out := make([]core.ActionResult, 0, len(results))
	for _, result := range results {
		out = append(out, redactActionResult(result))
	}
	return out
}

func sanitizeMessages(history []core.ConversationMessage) []core.ConversationMessage {
	out := make([]core.ConversationMessage, 0, len(history))
	for _, msg := range history {
		if msg.Role == "user" && strings.Contains(msg.Content, "Action results:\n") {
			prefix := "Action results:\n"
			body := strings.TrimPrefix(msg.Content, prefix)
			idx := strings.Index(body, "\nReturn a finish action only if the task is complete")
			if idx < 0 {
				idx = strings.Index(body, "\nIf the issue is resolved")
			}
			if idx >= 0 {
				resultsJSON := body[:idx]
				var results []core.ActionResult
				if err := json.Unmarshal([]byte(resultsJSON), &results); err == nil {
					safeJSON, _ := json.Marshal(sanitizeResults(results))
					msg.Content = prefix + string(safeJSON) + body[idx:]
				}
			}
		}
		if strings.Contains(msg.Content, "User follow-up while blocked:") {
			msg.Content = strings.ReplaceAll(msg.Content, "[secret]", "[secret]")
		}
		if strings.Contains(msg.Content, "Background run status:\n") {
			msg.Content = redactBackgroundContinuation(msg.Content)
		}
		out = append(out, msg)
	}
	return out
}

func redactBackgroundContinuation(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "summary=") {
			lines[i] = "summary=" + strings.TrimSpace(strings.TrimPrefix(line, "summary="))
		}
	}
	return strings.Join(lines, "\n")
}

func redactActionResult(result core.ActionResult) core.ActionResult {
	if result.Action.Type == core.ActionWriteFile && result.Action.Content != "" {
		result.Action.Content = fmt.Sprintf("[omitted %d bytes]", len(result.Action.Content))
	}
	if !result.IsSensitive {
		return result
	}
	result.InputValue = "[secret]"
	if result.Stdout != "" {
		result.Stdout = "[secret]"
	}
	if result.Stderr != "" {
		result.Stderr = "[secret]"
	}
	if result.Error != "" {
		result.Error = "[secret]"
	}
	result.Summary = blockerSummaryForStorage(result)
	return result
}

func blockerSummaryForStorage(result core.ActionResult) string {
	switch result.ResponseMode {
	case core.InputResponseSkipped:
		return "user skipped input"
	case core.InputResponseFollowUp:
		return "user added follow-up clarification"
	default:
		if result.IsSensitive {
			return "user provided secret input"
		}
		return result.Summary
	}
}

func (e *Engine) thinkingLabel() string {
	if e.Runtime.Interaction == core.InteractionPlanFirst {
		return "Planning..."
	}
	return "Thinking..."
}

func (e *Engine) thinkingLabelWithPhase(phase string) string {
	if strings.TrimSpace(phase) != "" {
		return phase
	}
	return e.thinkingLabel()
}

func buildInitialPrompt(prompt string, snapshot ctxpkg.Snapshot, graphDigest, opsDigest string) string {
	if strings.TrimSpace(graphDigest) == "" && strings.TrimSpace(opsDigest) == "" {
		return fmt.Sprintf("User request:\n%s\n\nCurrent context:\n%s", prompt, snapshot.JSON())
	}
	if strings.TrimSpace(opsDigest) == "" {
		return fmt.Sprintf("User request:\n%s\n\nCurrent context:\n%s\n\nServer graph memory:\n%s", prompt, snapshot.JSON(), graphDigest)
	}
	if strings.TrimSpace(graphDigest) == "" {
		return fmt.Sprintf("User request:\n%s\n\nCurrent context:\n%s\n\nOperational memory:\n%s", prompt, snapshot.JSON(), opsDigest)
	}
	return fmt.Sprintf("User request:\n%s\n\nCurrent context:\n%s\n\nServer graph memory:\n%s\n\nOperational memory:\n%s", prompt, snapshot.JSON(), graphDigest, opsDigest)
}

func (e *Engine) prepareTaskHistory(prompt string, snapshot ctxpkg.Snapshot, graphDigest, opsDigest string) (threads.ThreadRef, *threads.Thread, []core.ConversationMessage, error) {
	threadRef, threadState, err := e.resolveThreadContext()
	if err != nil {
		return threads.ThreadRef{}, nil, nil, err
	}
	history := []core.ConversationMessage{}
	if threadState != nil {
		history = append(history, threads.PromptMessages(*threadState, threads.PromptOptions{
			RecentTurns: e.Runtime.Config.Thread.RecentTurns,
		})...)
		e.warnContextUsage(*threadState, threadRef.Source)
	} else {
		// Even for ephemeral threads, register the limit so the UI shows the bar
		e.Renderer.SetContextUsage(0, e.Runtime.Config.Thread.MaxTokens)
	}
	history = append(history, core.ConversationMessage{
		Role:    "user",
		Content: buildInitialPrompt(prompt, snapshot, graphDigest, opsDigest),
	})
	return threadRef, threadState, history, nil
}

func (e *Engine) ensureGraphDigest(ctx context.Context, mode string) string {
	if e.Graph == nil || !e.Graph.Enabled() {
		return ""
	}
	if _, _, err := e.Graph.EnsureFresh(ctx, mode); err != nil {
		return ""
	}
	return e.Graph.Digest(6)
}

func (e *Engine) ensureOpsDigest() string {
	if e.Ops == nil {
		return ""
	}
	return e.Ops.SummaryLine()
}

func (e *Engine) opsRuntimeDeps() ops.RuntimeDeps {
	host := ""
	if e.Graph != nil {
		host = e.Graph.HostKey()
	}
	if host == "" {
		userName, rawHost := agent.CurrentIdentity()
		host = userName + "@" + rawHost
	}
	return ops.RuntimeDeps{
		Runtime:    e.Runtime,
		Graph:      e.Graph,
		Executor:   e.Executor,
		Policy:     e.PolicyStore,
		Host:       host,
		WorkingDir: e.Runtime.WorkingDir,
	}
}

func usageOrEstimate(request provider.Request, response core.LLMResponse) core.TokenUsage {
	if !response.Usage.Empty() {
		return response.Usage
	}
	chars := len(request.System)
	for _, msg := range request.Messages {
		chars += len(msg.Role) + len(msg.Content)
	}
	raw, _ := json.Marshal(response)
	chars += len(raw)
	estimated := chars / 4
	if estimated <= 0 {
		estimated = 1
	}
	return core.TokenUsage{
		TotalTokens: estimated,
		Estimated:   true,
	}
}

func (e *Engine) resolveThreadContext() (threads.ThreadRef, *threads.Thread, error) {
	if e.Runtime.NoContext || !e.Runtime.Config.Thread.EnabledValue() {
		return threads.ThreadRef{}, nil, nil
	}
	ref, err := threads.ResolveDefaultThread(e.Runtime)
	if err != nil {
		return threads.ThreadRef{}, nil, err
	}
	threadState, err := e.Threads.Load(ref.ThreadID)
	if err != nil {
		return threads.ThreadRef{}, nil, err
	}
	if threadState.ID == "" {
		threadState = threads.NewThread(ref)
	}
	if threadState.Scope == "" {
		threadState.Scope = ref.Scope
	}
	if threadState.ScopeKey == "" {
		threadState.ScopeKey = ref.ScopeKey
	}
	if threadState.CWD == "" {
		threadState.CWD = ref.WorkingDir
	}
	if threadState.Host == "" {
		threadState.Host = ref.Host
	}
	if threadState.User == "" {
		threadState.User = ref.User
	}
	if threadState.ParentPID == 0 {
		threadState.ParentPID = ref.ParentPID
	}
	if threadState.ParentStart == "" {
		threadState.ParentStart = ref.ParentStart
	}
	return ref, &threadState, nil
}

func (e *Engine) warnContextUsage(thread threads.Thread, source string) {
	maxTokens := e.Runtime.Config.Thread.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 12000
	}
	warnAt := int(float64(maxTokens) * 0.8)
	if ratio := e.Runtime.Config.Thread.WarnAtRatio; ratio > 0 && ratio < 1 {
		warnAt = int(float64(maxTokens) * ratio)
	}
	// Always keep the renderer's context bar up-to-date.
	e.Renderer.SetContextUsage(thread.EstimatedTokens, maxTokens)
	if thread.EstimatedTokens >= warnAt && !e.Runtime.JSONOutput {
		e.Renderer.Message(fmt.Sprintf("Context warning: thread %s (%s) is using %d/%d estimated tokens.", thread.ID, source, thread.EstimatedTokens, maxTokens))
	}
}

func newRecordID() string {
	host, _ := os.Hostname()
	host = strings.ReplaceAll(host, " ", "-")
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405.000000000"), host)
}
