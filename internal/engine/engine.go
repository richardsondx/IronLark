package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	ctxpkg "github.com/richardson/lark/internal/context"
	"github.com/richardson/lark/internal/core"
	"github.com/richardson/lark/internal/executor"
	"github.com/richardson/lark/internal/provider"
	"github.com/richardson/lark/internal/render"
	"github.com/richardson/lark/internal/sessions"
	"github.com/richardson/lark/internal/state"
)

type Engine struct {
	Runtime   state.Runtime
	Collector *ctxpkg.Collector
	Executor  *executor.Executor
	Provider  provider.Client
	Renderer  *render.Renderer
	Sessions  sessions.Store
}

func (e *Engine) RunTask(ctx context.Context, prompt string, stdin []byte, mode string) error {
	if e.Provider == nil {
		return fmt.Errorf("provider is not configured or API key is unavailable")
	}

	snapshot, err := e.Collector.CollectMinimal(ctx, e.Runtime, stdin)
	if err != nil {
		return err
	}

	history := []core.ConversationMessage{
		{
			Role:    "user",
			Content: buildInitialPrompt(prompt, snapshot),
		},
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
		response, err := e.Provider.Generate(ctx, provider.Request{
			Model:       e.Runtime.Model,
			System:      provider.BuildSystemPrompt(e.Runtime.Config.Context.MaxActions),
			Messages:    history,
			Temperature: 0.1,
		})
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
			history[0].Content = buildInitialPrompt(prompt, fullSnapshot)
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
		record.Results = append(record.Results, results...)
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
		resultsJSON, _ := json.Marshal(results)
		history = append(history, core.ConversationMessage{
			Role: "user",
			Content: "Action results:\n" + string(resultsJSON) +
				"\nIf the issue is resolved, finish. Otherwise propose the next smallest safe step.",
		})
	}

	record.Summary = finalSummary
	record.Messages = history
	record.FinishedAt = time.Now().UTC()
	return e.Sessions.Save(record)
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

	var history []core.ConversationMessage
	if initialPrompt != "" {
		if err := e.runChatPrompt(ctx, initialPrompt, stdin, &history, &record); err != nil {
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
				e.Renderer.Message("Chat history cleared. Starting fresh.")
				continue
			case "/help":
				e.Renderer.Message("Available slash commands:\n  /model [name]    - Get or set the current model\n  /provider [name] - Get or set the current provider\n  /clear           - Clear the conversation history\n  /help            - Show this menu")
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
		if err := e.runChatPrompt(ctx, prompt, nil, &history, &record); err != nil {
			return err
		}
	}
}

func (e *Engine) runChatPrompt(ctx context.Context, prompt string, stdin []byte, history *[]core.ConversationMessage, record *sessions.Record) error {
	snapshot, err := e.Collector.Collect(ctx, e.Runtime, stdin)
	if err != nil {
		return err
	}
	*history = append(*history, core.ConversationMessage{
		Role:    "user",
		Content: buildInitialPrompt(prompt, snapshot),
	})
	response, err := e.Provider.Generate(ctx, provider.Request{
		Model:       e.Runtime.Model,
		System:      provider.BuildSystemPrompt(e.Runtime.Config.Context.MaxActions),
		Messages:    *history,
		Temperature: 0.1,
	})
	if err != nil {
		return err
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
	record.Results = append(record.Results, results...)
	rawResponse, _ := json.Marshal(response)
	*history = append(*history, core.ConversationMessage{
		Role:    "assistant",
		Content: string(rawResponse),
	})
	if len(results) > 0 {
		resultsJSON, _ := json.Marshal(results)
		*history = append(*history, core.ConversationMessage{
			Role: "user",
			Content: "Action results:\n" + string(resultsJSON) +
				"\nSummarize the outcome briefly and only ask for more actions if needed.",
		})
	}
	record.Messages = *history
	record.FinishedAt = time.Now().UTC()
	return e.Sessions.Save(*record)
}

func (e *Engine) executeTurn(ctx context.Context, response core.LLMResponse) ([]core.ActionResult, bool, error) {
	if len(response.Actions) == 0 {
		return nil, true, nil
	}

	previews := make([]core.RiskReport, 0, len(response.Actions))
	needApproval := false
	for _, action := range response.Actions {
		report, err := e.Executor.Preview(action, e.Runtime.ReadOnly)
		if err != nil {
			return nil, false, err
		}
		previews = append(previews, report)
		if e.Executor.Classifier.NeedsApproval(action, report, e.Runtime.ApprovalMode, e.Runtime.Config.Security.AutoApproveReadTools, e.Runtime.ReadOnly) {
			needApproval = true
		}
	}
	e.Renderer.PlannedActions(response.Actions, previews)
	if len(response.Verification) > 0 && !e.Runtime.JSONOutput {
		e.Renderer.Message("\nVerification planned after execution.")
	}

	if e.Runtime.ApprovalMode == core.ApprovalSuggest {
		return nil, true, nil
	}

	strategy := "all"
	if needApproval {
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
		if action.Type == core.ActionFinish {
			return results, true, nil
		}
		if action.Type == core.ActionInspect {
			continue
		}
		if action.Type == core.ActionAskUser {
			answer, err := e.Renderer.ReadPrompt(action.Reason + ": ")
			if err != nil {
				return results, false, err
			}
			results = append(results, core.ActionResult{
				Action:   action,
				Risk:     report,
				Approved: true,
				Stdout:   answer,
				Summary:  answer,
			})
			continue
		}

		approved := true
		if e.Executor.Classifier.NeedsApproval(action, report, e.Runtime.ApprovalMode, e.Runtime.Config.Security.AutoApproveReadTools, e.Runtime.ReadOnly) {
			if strategy == "step" || e.Executor.Classifier.RequiresDoubleConfirm(report) {
				ok, err := e.Renderer.Confirm(action.Title, e.Executor.Classifier.RequiresDoubleConfirm(report))
				if err != nil {
					return results, false, err
				}
				approved = ok
			}
		}
		if !approved {
			results = append(results, core.ActionResult{
				Action:   action,
				Risk:     report,
				Skipped:  true,
				Summary:  "user declined action",
				Approved: false,
			})
			continue
		}
		result, err := e.Executor.Execute(ctx, action, e.Runtime.ReadOnly)
		e.Renderer.Result(result)
		results = append(results, result)
		if err != nil {
			return results, false, nil
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
			result, err := e.Executor.Execute(ctx, action, true)
			e.Renderer.Result(result)
			results = append(results, result)
			if err != nil {
				break
			}
		}
	}

	return results, false, nil
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

func buildInitialPrompt(prompt string, snapshot ctxpkg.Snapshot) string {
	return fmt.Sprintf("User request:\n%s\n\nCurrent context:\n%s", prompt, snapshot.JSON())
}

func newRecordID() string {
	host, _ := os.Hostname()
	host = strings.ReplaceAll(host, " ", "-")
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405.000000000"), host)
}
