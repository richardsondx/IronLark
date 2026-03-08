package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/richardsondx/IronLark/internal/agent"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/graph"
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

	threadRef, threadState, history, err := e.prepareTaskHistory(prompt, snapshot, graphDigest)
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

	var finalSummary string
	isMinimalContext := true
	consecutiveFailures := 0
	maxTurns := e.Runtime.Config.Tools.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 5
	}
	for turn := 0; turn < maxTurns; turn++ {
		e.Renderer.BeginThinking(e.thinkingLabel())
		response, err := e.Provider.Generate(ctx, provider.Request{
			Model:       e.Runtime.Model,
			System:      provider.BuildSystemPrompt(e.Runtime.Config.Context.MaxActions, e.Runtime.Interaction),
			Messages:    history,
			Temperature: 0.1,
		})
		e.Renderer.EndThinking()
		if err != nil {
			return err
		}

		// Check if the model wants to inspect or if we are doing actions for the first time on minimal context
		needsFullContext := false
		if isMinimalContext && len(response.Actions) > 0 {
			needsFullContext = true
		}

		if needsFullContext && isMinimalContext {
			if !e.Runtime.JSONOutput {
				e.Renderer.Message("Inspecting context and reframing plan...")
			}
			fullSnapshot, err := e.Collector.Collect(ctx, e.Runtime, stdin)
			if err != nil {
				return err
			}
			isMinimalContext = false
			record.ContextJSON = fullSnapshot.JSON()

			// Replace initial user message with full context and re-run the generation
			history[len(history)-1].Content = buildInitialPrompt(prompt, fullSnapshot, graphDigest)
			continue
		}

		record.Findings = append(record.Findings, response.Findings...)
		record.Actions = append(record.Actions, response.Actions...)
		record.NeedsUserInput = response.NeedsUserInput
		record.Confidence = response.Confidence
		finalSummary = response.Summary

		if err := e.Renderer.Response(response); err != nil {
			return err
		}

		results, stop, err := e.executeTurn(ctx, response)
		if err != nil {
			return err
		}
		record.Results = append(record.Results, sanitizeResults(results)...)
		if hasErrors(results) {
			consecutiveFailures++
		} else {
			consecutiveFailures = 0
		}

		rawResponse, _ := json.Marshal(response)
		history = append(history, core.ConversationMessage{
			Role:    "assistant",
			Content: string(rawResponse),
		})

		if stop || len(results) == 0 || reachedFailureLimit(consecutiveFailures, e.Runtime.Config.Tools.MaxConsecutiveFailures) {
			break
		}
		history = append(history, buildContinuationMessage(results))
	}

	record.Summary = finalSummary
	record.Messages = sanitizeMessages(history)
	record.FinishedAt = time.Now().UTC()
	if err := e.Sessions.Save(record); err != nil {
		return err
	}
	if threadState != nil {
		*threadState = e.Threads.AppendTurn(*threadState, prompt, core.LLMResponse{
			Summary:  finalSummary,
			Findings: record.Findings,
			Actions:  record.Actions,
		}, record.Results, threads.AppendOptions{
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
			return err
		}
	}

	for {
		promptRaw, err := e.Renderer.ReadPrompt("> ")
		if err != nil {
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
					e.Renderer.Message(fmt.Sprintf("Model set to: %s", e.Runtime.Model))
				} else {
					e.Renderer.Message(fmt.Sprintf("Current model: %s. Use /model <name> to change.", e.Runtime.Model))
				}
				continue
			case "/provider":
				if len(args) > 0 {
					e.Runtime.ProviderName = args[0]

					// Re-initialize provider
					if providerCfg, err := e.Runtime.ProviderConfig(); err == nil {
						if apiKey, keyErr := e.Runtime.APIKey(); keyErr == nil {
							if providerCfg.Type == "openai-compatible" {
								e.Provider = provider.OpenAICompatibleFactory{}.New(providerCfg.BaseURL, apiKey, providerCfg.Headers)
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
			case "/exit":
				record.FinishedAt = time.Now().UTC()
				return e.Sessions.Save(record)
			case "/help":
				e.Renderer.Message("Available slash commands:\n  /mode [name]      - Get or set execute-first / plan-first\n  /approval [name]  - Get or set confirm / auto-safe / agent / suggest\n  /secret [state]   - Get or set secret input visibility\n  /model [name]     - Get or set the current model\n  /provider [name]  - Get or set the current provider\n  /clear            - Clear the conversation history\n  /help             - Show this menu\n  /exit             - Exit the agent session")
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
			return err
		}
	}
}

func (e *Engine) runChatPrompt(ctx context.Context, prompt string, stdin []byte, history *[]core.ConversationMessage, record *sessions.Record, threadState *threads.Thread, threadRef threads.ThreadRef) error {
	snapshot, err := e.Collector.Collect(ctx, e.Runtime, stdin)
	if err != nil {
		return err
	}
	graphDigest := e.ensureGraphDigest(ctx, graph.ModeLight)
	*history = append(*history, core.ConversationMessage{
		Role:    "user",
		Content: buildInitialPrompt(prompt, snapshot, graphDigest),
	})
	e.Renderer.BeginThinking(e.thinkingLabel())
	response, stopped, err := e.generateResponse(ctx, provider.Request{
		Model:       e.Runtime.Model,
		System:      provider.BuildSystemPrompt(e.Runtime.Config.Context.MaxActions, e.Runtime.Interaction),
		Messages:    *history,
		Temperature: 0.1,
	})
	e.Renderer.EndThinking()
	if err != nil {
		return err
	}
	if stopped {
		return errTurnStopped
	}
	record.Prompt += "\n" + prompt
	record.ContextJSON = snapshot.JSON()
	record.Findings = append(record.Findings, response.Findings...)
	record.Actions = append(record.Actions, response.Actions...)
	record.Summary = response.Summary
	record.NeedsUserInput = response.NeedsUserInput
	record.Confidence = response.Confidence
	if err := e.Renderer.Response(response); err != nil {
		return err
	}
	results, _, err := e.executeTurn(ctx, response)
	if err != nil {
		return err
	}
	record.Results = append(record.Results, sanitizeResults(results)...)
	rawResponse, _ := json.Marshal(response)
	*history = append(*history, core.ConversationMessage{
		Role:    "assistant",
		Content: string(rawResponse),
	})
	if len(results) > 0 {
		*history = append(*history, buildContinuationMessage(results))
	}
	record.Messages = sanitizeMessages(*history)
	record.FinishedAt = time.Now().UTC()
	if err := e.Sessions.Save(*record); err != nil {
		return err
	}
	if threadState != nil {
		*threadState = e.Threads.AppendTurn(*threadState, prompt, response, results, threads.AppendOptions{
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

	response, err := e.Provider.Generate(generateCtx, request)
	cancel()
	<-done
	if err != nil {
		if stopped.Load() && errors.Is(err, context.Canceled) {
			return core.LLMResponse{}, true, nil
		}
		return core.LLMResponse{}, false, err
	}
	return response, false, nil
}

func (e *Engine) executeTurn(ctx context.Context, response core.LLMResponse) ([]core.ActionResult, bool, error) {
	if len(response.Actions) == 0 && len(response.Verification) == 0 {
		return nil, true, nil
	}

	previews := make([]core.RiskReport, 0, len(response.Actions))
	matches := make([]policy.Match, 0, len(response.Actions))
	needApproval := false
	for _, action := range response.Actions {
		report, err := e.Executor.Preview(action, e.Runtime.ReadOnly)
		if err != nil {
			return nil, false, err
		}
		match, err := e.PolicyStore.Evaluate(action)
		if err != nil {
			return nil, false, err
		}
		previews = append(previews, report)
		matches = append(matches, match)
		if e.actionNeedsApproval(action, report, match) {
			needApproval = true
		}
	}
	if e.Runtime.Interaction == core.InteractionPlanFirst {
		e.Renderer.PlannedActions(response.Actions, previews)
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
		match := matches[idx]
		if action.Type == core.ActionFinish {
			return results, true, nil
		}
		if action.Type == core.ActionInspect {
			continue
		}
		if action.Type == core.ActionAskUser {
			result, err := e.Renderer.CollectUserInput(action)
			if err != nil {
				return results, false, err
			}
			result.Action = action
			result.Risk = report
			result.Approved = true
			e.Renderer.Result(redactActionResult(result))
			results = append(results, result)
			continue
		}
		if match.Matched && match.Decision == policy.DecisionDeny {
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

		result, stop, err := e.executeAction(ctx, action, report, match, strategy, e.Runtime.ReadOnly, e.Runtime.Interaction == core.InteractionExecuteFirst)
		if err != nil {
			return results, false, nil
		}
		if stop {
			return results, true, nil
		}
		results = append(results, result)
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
			match, err := e.PolicyStore.Evaluate(action)
			if err != nil {
				return results, false, err
			}
			result, stop, err := e.executeAction(ctx, action, report, match, strategy, e.Runtime.ReadOnly, false)
			if err != nil {
				return results, false, nil
			}
			results = append(results, result)
			if stop {
				return results, true, nil
			}
		}
	}

	return results, false, nil
}

func (e *Engine) executeAction(ctx context.Context, action core.Action, report core.RiskReport, match policy.Match, strategy string, readOnly bool, showProgress bool) (core.ActionResult, bool, error) {
	if match.Matched && match.Decision == policy.DecisionDeny {
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
		return result, false, nil
	}

	approved := true
	if e.actionNeedsApproval(action, report, match) {
		decision, err := e.promptForActionDecision(action, report, strategy)
		if err != nil {
			return core.ActionResult{}, false, err
		}
		switch decision {
		case "allow-once":
			approved = true
		case "allow-always":
			approved = true
			if _, err := e.PolicyStore.Add(policy.RuleForAction(action, policy.DecisionAllow)); err != nil {
				return core.ActionResult{}, false, err
			}
		case "deny-once":
			approved = false
		case "cancel":
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
		return result, false, nil
	}
	if showProgress && !e.Runtime.JSONOutput {
		e.Renderer.ActionProgress(action)
	}
	result, err := e.Executor.Execute(ctx, action, readOnly)
	e.Renderer.Result(result)
	return result, false, err
}

func (e *Engine) actionNeedsApproval(action core.Action, report core.RiskReport, match policy.Match) bool {
	if match.Matched && match.Decision == policy.DecisionAllow && report.Level != core.RiskHigh {
		return false
	}
	if e.Executor.Classifier.IsSensitiveAction(action) {
		return true
	}
	return e.Executor.Classifier.NeedsApproval(action, report, e.Runtime.ApprovalMode, e.Runtime.Config.Security.AutoApproveReadTools, e.Runtime.ReadOnly)
}

func (e *Engine) promptForActionDecision(action core.Action, report core.RiskReport, strategy string) (string, error) {
	if e.Runtime.Interaction == core.InteractionPlanFirst && strategy == "all" && !e.Executor.Classifier.RequiresDoubleConfirm(report) {
		return "allow-once", nil
	}
	if e.Runtime.Interaction == core.InteractionPlanFirst && strategy != "step" && e.Executor.Classifier.RequiresDoubleConfirm(report) {
		ok, err := e.Renderer.Confirm(action.Title, true)
		if err != nil {
			return "", err
		}
		if ok {
			return "allow-once", nil
		}
		return "deny-once", nil
	}
	e.Renderer.ApprovalPrompt(action, report)
	choice, err := e.Renderer.PromptApprovalChoice()
	if err != nil {
		return "", err
	}
	switch choice {
	case "1":
		return "allow-once", nil
	case "2":
		return "allow-always", nil
	case "3":
		return "deny-once", nil
	default:
		return "cancel", nil
	}
}

func hasErrors(results []core.ActionResult) bool {
	for _, result := range results {
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
	resultsJSON, _ := json.Marshal(results)
	return core.ConversationMessage{
		Role: "user",
		Content: "Action results:\n" + string(resultsJSON) +
			"\nIf the issue is resolved, finish. Otherwise propose the next smallest safe step.",
	}
}

func findFollowUp(results []core.ActionResult) *core.ActionResult {
	for i := range results {
		if results[i].ResponseMode == core.InputResponseFollowUp {
			return &results[i]
		}
	}
	return nil
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
			if idx := strings.Index(body, "\nIf the issue is resolved"); idx >= 0 {
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
		out = append(out, msg)
	}
	return out
}

func redactActionResult(result core.ActionResult) core.ActionResult {
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

func buildInitialPrompt(prompt string, snapshot ctxpkg.Snapshot, graphDigest string) string {
	if strings.TrimSpace(graphDigest) == "" {
		return fmt.Sprintf("User request:\n%s\n\nCurrent context:\n%s", prompt, snapshot.JSON())
	}
	return fmt.Sprintf("User request:\n%s\n\nCurrent context:\n%s\n\nServer graph memory:\n%s", prompt, snapshot.JSON(), graphDigest)
}

func (e *Engine) prepareTaskHistory(prompt string, snapshot ctxpkg.Snapshot, graphDigest string) (threads.ThreadRef, *threads.Thread, []core.ConversationMessage, error) {
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
	}
	history = append(history, core.ConversationMessage{
		Role:    "user",
		Content: buildInitialPrompt(prompt, snapshot, graphDigest),
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
	if thread.EstimatedTokens >= warnAt && !e.Runtime.JSONOutput {
		e.Renderer.Message(fmt.Sprintf("Context warning: thread %s (%s) is using %d/%d estimated tokens.", thread.ID, source, thread.EstimatedTokens, maxTokens))
	}
}

func newRecordID() string {
	host, _ := os.Hostname()
	host = strings.ReplaceAll(host, " ", "-")
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405.000000000"), host)
}
