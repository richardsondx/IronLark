package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/graph"
	"github.com/richardsondx/IronLark/internal/ops"
	"github.com/richardsondx/IronLark/internal/patches"
	policypkg "github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/search"
	"github.com/richardsondx/IronLark/internal/sessions"
	"github.com/richardsondx/IronLark/internal/state"
	"github.com/richardsondx/IronLark/internal/threads"
)

func TestExecuteTurnExecuteFirstSkipsVisiblePlan(t *testing.T) {
	engine, output := testEngine(t, core.InteractionExecuteFirst)
	response := core.LLMResponse{
		Summary: "Checking current directory.",
		Actions: []core.Action{{
			ID:      "1",
			Type:    core.ActionRunShell,
			Title:   "pwd",
			Command: "pwd",
			Reason:  "answer the question",
		}},
	}

	_, _, err := engine.executeTurn(context.Background(), response)
	if err != nil {
		t.Fatalf("executeTurn() error = %v", err)
	}
	if strings.Contains(output.String(), "Proposed actions") {
		t.Fatalf("execute-first should not render proposed actions, got %q", output.String())
	}
	if !strings.Contains(output.String(), "[run_shell] pwd") {
		t.Fatalf("expected action progress output, got %q", output.String())
	}
}

func TestExecuteTurnPlanFirstShowsVisiblePlan(t *testing.T) {
	engine, output := testEngine(t, core.InteractionPlanFirst)
	response := core.LLMResponse{
		Summary: "Checking current directory.",
		Actions: []core.Action{{
			ID:      "1",
			Type:    core.ActionRunShell,
			Title:   "pwd",
			Command: "pwd",
			Reason:  "answer the question",
		}},
	}

	_, _, err := engine.executeTurn(context.Background(), response)
	if err != nil {
		t.Fatalf("executeTurn() error = %v", err)
	}
	if !strings.Contains(output.String(), "Proposed actions") {
		t.Fatalf("plan-first should render proposed actions, got %q", output.String())
	}
}

func TestSensitiveReadNeedsApproval(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	action := core.Action{Type: core.ActionReadFiles, Path: "/tmp/project/.env"}
	report, err := engine.Executor.Preview(action, false)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if !engine.actionNeedsApproval(action, report, policypkg.Resolution{}) {
		t.Fatalf("expected sensitive read to require approval")
	}
}

func TestAllowRuleSuppressesApproval(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	action := core.Action{Type: core.ActionRunShell, Command: "systemctl restart nginx"}
	report, err := engine.Executor.Preview(action, false)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if report.Level != core.RiskMedium {
		t.Fatalf("expected medium risk report, got %s", report.Level)
	}
	if engine.actionNeedsApproval(action, report, policypkg.Resolution{Match: policypkg.Match{Matched: true, Decision: policypkg.DecisionAllow}}) {
		t.Fatalf("expected allow rule to suppress approval")
	}
}

func TestAutoAcceptThresholdSuppressesApproval(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	action := core.Action{Type: core.ActionRunShell, Command: "systemctl restart nginx"}
	report, err := engine.Executor.Preview(action, false)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if !engine.actionNeedsApproval(action, report, policypkg.Resolution{}) {
		t.Fatalf("expected baseline medium-risk action to require approval")
	}
	if engine.actionNeedsApproval(action, report, policypkg.Resolution{AutoAcceptThrough: core.RiskMedium}) {
		t.Fatalf("expected medium auto-accept threshold to suppress approval")
	}
	if !engine.actionNeedsApproval(action, report, policypkg.Resolution{AutoAcceptThrough: core.RiskLow}) {
		t.Fatalf("expected low auto-accept threshold to still require approval")
	}
}

func TestAutoAcceptHighSuppressesHighRiskApproval(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	action := core.Action{Type: core.ActionRunShell, Command: "rm -rf /tmp/bad"}
	report, err := engine.Executor.Preview(action, false)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if report.Level != core.RiskHigh {
		t.Fatalf("expected high risk report, got %s", report.Level)
	}
	if engine.actionNeedsApproval(action, report, policypkg.Resolution{AutoAcceptThrough: core.RiskHigh}) {
		t.Fatalf("expected high auto-accept threshold to suppress approval")
	}
}

func TestShouldPromoteShellBeforeRunForLongHeuristic(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	engine.Ops = &ops.Manager{}
	if !engine.shouldPromoteShellBeforeRun(core.Action{
		Type:    core.ActionRunShell,
		Command: "pipx install harbor",
	}) {
		t.Fatal("expected install heuristic to promote to background")
	}
}

func TestHasErrorsIgnoresBackgroundRuns(t *testing.T) {
	if hasErrors([]core.ActionResult{{
		Error:           "signal: killed",
		BackgroundRunID: "shell-1",
	}}) {
		t.Fatal("expected backgrounded result not to count as a blocking error")
	}
}

func TestBuildContinuationMessageBlocksPrematureVerificationForBackgroundRun(t *testing.T) {
	msg := buildContinuationMessage([]core.ActionResult{{
		BackgroundRunID: "shell-1",
		Summary:         "continuing in background run shell-1 (pid 123)",
	}})
	if !strings.Contains(msg.Content, "Background run status:") {
		t.Fatalf("expected background status continuation, got %q", msg.Content)
	}
	if strings.Contains(msg.Content, "If the issue is resolved, finish") {
		t.Fatalf("expected no generic finish/verify continuation, got %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "Do not verify completion-dependent outcomes yet") {
		t.Fatalf("expected explicit wait guidance, got %q", msg.Content)
	}
}

func TestBuildContinuationMessageRequiresExplicitFinishOrNextAction(t *testing.T) {
	msg := buildContinuationMessage([]core.ActionResult{{
		Action:   core.Action{Type: core.ActionRunShell, Title: "Inspect"},
		Summary:  "inspect complete",
		Approved: true,
	}})
	if !strings.Contains(msg.Content, "Return a finish action only if the task is complete") {
		t.Fatalf("expected explicit finish guidance, got %q", msg.Content)
	}
}

func TestDenyRuleBlocksExecution(t *testing.T) {
	engine, output := testEngine(t, core.InteractionExecuteFirst)
	rule := policypkg.RuleForAction(core.Action{Type: core.ActionRunShell, Command: "systemctl restart nginx"}, policypkg.DecisionDeny)
	if _, err := engine.PolicyStore.Add(rule); err != nil {
		t.Fatalf("policy add error = %v", err)
	}
	response := core.LLMResponse{
		Summary: "Restarting service.",
		Actions: []core.Action{{
			ID:      "1",
			Type:    core.ActionRunShell,
			Title:   "restart",
			Command: "systemctl restart nginx",
			Reason:  "apply change",
		}},
	}

	results, _, err := engine.executeTurn(context.Background(), response)
	if err != nil {
		t.Fatalf("executeTurn() error = %v", err)
	}
	if len(results) != 1 || !results[0].Skipped || results[0].Summary != "blocked by machine policy" {
		t.Fatalf("expected blocked result, got %#v", results)
	}
	if !strings.Contains(output.String(), "blocked by machine policy") {
		t.Fatalf("expected blocked output, got %q", output.String())
	}
}

func TestRunTaskTriggersThinkingLifecycle(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{Summary: "Done"}}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "hello", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if renderer.beginCalls == 0 || renderer.endCalls == 0 {
		t.Fatalf("expected thinking lifecycle, got begin=%d end=%d", renderer.beginCalls, renderer.endCalls)
	}
}

func TestGenerateResponseRetriesEmptySummary(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	fake := &fakeProvider{responses: []core.LLMResponse{
		{},
		{Summary: "The project is a single Go CLI with most code under internal/."},
	}}
	engine.Provider = fake

	response, stopped, err := engine.generateResponse(context.Background(), provider.Request{
		Model:       "gpt-5",
		System:      "test",
		Messages:    []core.ConversationMessage{{Role: "user", Content: "how is this project setup?"}},
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if stopped {
		t.Fatal("expected non-stopped response")
	}
	if response.Summary == "" {
		t.Fatalf("expected non-empty summary after retry, got %#v", response)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("expected retry on empty summary, got %d requests", len(fake.requests))
	}
	lastMessages := fake.requests[1].Messages
	if len(lastMessages) == 0 || !strings.Contains(lastMessages[len(lastMessages)-1].Content, "empty summary") {
		t.Fatalf("expected corrective retry prompt, got %#v", lastMessages)
	}
}

func TestGenerateResponseFallsBackWhenSummaryStaysEmpty(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{Actions: []core.Action{{ID: "1", Type: core.ActionRunShell, Title: "Inspect repo", Reason: "Inspect the repo layout."}}},
		{Actions: []core.Action{{ID: "1", Type: core.ActionRunShell, Title: "Inspect repo", Reason: "Inspect the repo layout."}}},
	}}

	response, stopped, err := engine.generateResponse(context.Background(), provider.Request{
		Model:       "gpt-5",
		System:      "test",
		Messages:    []core.ConversationMessage{{Role: "user", Content: "how is this project setup?"}},
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("generateResponse() error = %v", err)
	}
	if stopped {
		t.Fatal("expected non-stopped response")
	}
	if response.Summary != "Inspect the repo layout." {
		t.Fatalf("expected fallback summary, got %#v", response)
	}
}

func TestRunChatPromptNormalizesEmptySummaryBeforeRendering(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{}}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "what is this?", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if record.Summary == "" {
		t.Fatalf("expected normalized summary in record, got %#v", record)
	}
	found := false
	for _, msg := range record.Messages {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "I wasn't able to produce a complete answer") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected normalized assistant message, got %#v", record.Messages)
	}
}

func TestRunChatPromptShortCircuitsSimpleGreeting(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{}
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "hello world", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if record.Summary != "Hi! How can I help you with your server or repo today?" {
		t.Fatalf("unexpected greeting summary %q", record.Summary)
	}
}

func TestRunChatPromptReplacesEmptyGreetingFallback(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{}, {}}}
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "hello world", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if record.Summary != "Hi! How can I help you with your server or repo today?" {
		t.Fatalf("unexpected greeting summary %q", record.Summary)
	}
}

func TestRunChatPromptRetriesWithDirectPromptAfterSyntheticFallback(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	fake := &fakeProvider{responses: []core.LLMResponse{
		{},
		{},
		{Summary: "Hello, world!"},
	}}
	engine.Provider = fake
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "hellow world", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if record.Summary != "Hello, world!" {
		t.Fatalf("unexpected recovered summary %q", record.Summary)
	}
	if len(fake.requests) != 3 {
		t.Fatalf("expected fallback recovery request, got %d requests", len(fake.requests))
	}
	lastMessages := fake.requests[2].Messages
	if len(lastMessages) != 1 || lastMessages[0].Content != "hellow world" {
		t.Fatalf("expected direct prompt retry, got %#v", lastMessages)
	}
}

func TestRunChatPromptEmitsNarrativeEventsWhenEnabled(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Runtime.Config.UI.NarratedProgress = true
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{
		Summary: "Inspecting tests.",
		Narration: &core.Narration{
			TurnIntent: "I found an existing test file, so I'll inspect it before editing.",
			ActionHints: []core.NarrationActionHint{{
				ActionID: "read-tests",
				Text:     "Let me read the current tests first.",
			}},
		},
		Actions: []core.Action{{
			ID:      "read-tests",
			Type:    core.ActionRunShell,
			Title:   "pwd",
			Command: "pwd",
			Reason:  "inspect current location",
		}},
	}}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "inspect tests", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if len(renderer.narrativeEvents) < 3 {
		t.Fatalf("expected narrative events, got %#v", renderer.narrativeEvents)
	}
	if renderer.narrativeEvents[0].Kind != core.NarrativeTurnStarted {
		t.Fatalf("expected first event to be turn_started, got %#v", renderer.narrativeEvents[0])
	}
	foundIntent := false
	for _, event := range renderer.narrativeEvents {
		if event.Kind == core.NarrativeIntent && strings.Contains(event.Text, "existing test file") {
			foundIntent = true
			break
		}
	}
	if !foundIntent {
		t.Fatalf("expected model-authored turn intent in events %#v", renderer.narrativeEvents)
	}
}

func TestRunChatPromptContinuesUntilClosingAnswer(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "I'll inspect the host for an OpenClaw install.",
			Actions: []core.Action{{
				ID:      "inspect-openclaw",
				Type:    core.ActionRunShell,
				Title:   "Detect OpenClaw",
				Command: "printf 'openclaw found\\n'",
				Reason:  "check the host",
			}},
		},
		{
			Summary: "I'll inspect the host for an OpenClaw install.",
			Actions: []core.Action{{
				ID:      "inspect-openclaw",
				Type:    core.ActionRunShell,
				Title:   "Detect OpenClaw",
				Command: "printf 'openclaw found\\n'",
				Reason:  "check the host",
			}},
		},
		{
			Summary: "OpenClaw appears to be installed on this host.",
			Findings: []string{
				"The shell check reported an OpenClaw installation marker.",
			},
			Actions: []core.Action{{
				ID:     "finish-openclaw",
				Type:   core.ActionFinish,
				Title:  "Finish",
				Reason: "task complete",
			}},
		},
	}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "is openclaw installed?", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if len(engine.Provider.(*fakeProvider).requests) < 2 {
		t.Fatalf("expected follow-up provider call after action results")
	}
	if record.Summary != "OpenClaw appears to be installed on this host." {
		t.Fatalf("expected final closing summary, got %q", record.Summary)
	}
	if len(renderer.responses) == 0 || renderer.responses[len(renderer.responses)-1].Summary != record.Summary {
		t.Fatalf("expected closing response to be rendered, got %#v", renderer.responses)
	}
}

func TestRunChatPromptExecuteFirstRunsActionWithoutFullContextRetry(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	provider := &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "Checking Docker.",
			Actions: []core.Action{{
				ID:      "check-docker",
				Type:    core.ActionRunShell,
				Title:   "Check Docker",
				Command: "printf 'docker ok\\n'",
				Reason:  "check whether Docker is installed",
			}},
		},
		{
			Summary: "Docker is installed.",
			Actions: []core.Action{{
				ID:     "finish-docker",
				Type:   core.ActionFinish,
				Title:  "Finish",
				Reason: "task complete",
			}},
		},
	}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "is docker installed?", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected action execution without full-context retry, got %d provider requests", len(provider.requests))
	}
}

func TestRunChatPromptSynthesizesClosingAnswerWhenModelStopsAfterResults(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "I'll inspect the host.",
			Actions: []core.Action{{
				ID:      "inspect-root",
				Type:    core.ActionRunShell,
				Title:   "List root",
				Command: "printf '/root/.openclaw\\n'",
				Reason:  "look for an installation directory",
			}},
		},
		{
			Summary: "I'll inspect the host.",
			Actions: []core.Action{{
				ID:      "inspect-root",
				Type:    core.ActionRunShell,
				Title:   "List root",
				Command: "printf '/root/.openclaw\\n'",
				Reason:  "look for an installation directory",
			}},
		},
		{},
		{},
	}}
	engine.Runtime.NoContext = true
	engine.Runtime.Config.Tools.MaxTurns = 2
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "inspect root", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if record.CompletionStatus != core.CompletionIncompleteMaxTurns {
		t.Fatalf("expected incomplete max-turn status, got %q", record.CompletionStatus)
	}
	if !strings.Contains(record.Summary, "I stopped before completion after reaching the hard turn cap.") {
		t.Fatalf("expected explicit incomplete summary, got %q", record.Summary)
	}
	if len(renderer.responses) == 0 || renderer.responses[len(renderer.responses)-1].Summary != record.Summary {
		t.Fatalf("expected synthesized response to render, got %#v", renderer.responses)
	}
}

func TestRunChatPromptResumesAfterStructuredUserInput(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{
		inputResults: []core.ActionResult{{
			InputKind:    core.InputText,
			FieldKey:     "token",
			ResponseMode: core.InputResponseSubmitted,
			InputValue:   "123:abc",
		}},
	}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "I need the token first.",
			Actions: []core.Action{{
				ID:           "ask-token",
				Type:         core.ActionAskUser,
				InputKind:    core.InputText,
				FieldKey:     "token",
				Prompt:       "Paste the token",
				Alternatives: []string{"submit", "skip", "follow_up"},
			}},
		},
		{
			Summary: "The token is in place and the task can continue.",
		},
	}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "configure openclaw", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if len(renderer.blockerActions) != 1 {
		t.Fatalf("expected one blocker action, got %d", len(renderer.blockerActions))
	}
	if record.Summary != "The token is in place and the task can continue." {
		t.Fatalf("expected final summary after input, got %q", record.Summary)
	}
}

func TestRunTaskFinishesOnExplicitFinishAction(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "Checking Docker.",
			Actions: []core.Action{{
				ID:      "check-docker",
				Type:    core.ActionRunShell,
				Title:   "Check Docker",
				Command: "printf 'docker ok\\n'",
				Reason:  "check Docker",
			}},
		},
		{
			Summary: "Docker is installed.",
			Actions: []core.Action{{
				ID:     "finish-docker",
				Type:   core.ActionFinish,
				Title:  "Finish",
				Reason: "the task is complete",
			}},
		},
	}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "is docker installed?", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	records, err := engine.Sessions.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 || records[0].CompletionStatus != core.CompletionFinished {
		t.Fatalf("expected finished completion status, got %#v", records)
	}
}

func TestRunTaskAllowsAnswerOnlyCompletionWithoutExecutedActions(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{
		Summary: "Docker is installed.",
	}}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "is docker installed?", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	records, err := engine.Sessions.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 || records[0].CompletionStatus != core.CompletionFinished {
		t.Fatalf("expected answer-only completion to finish, got %#v", records)
	}
}

func TestRunTaskContinuesPastSoftTurnsWhenProgressIsReal(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Runtime.Config.UI.NarratedProgress = true
	provider := &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "Check docker.",
			Actions: []core.Action{{
				ID:      "check-docker",
				Type:    core.ActionRunShell,
				Title:   "Check Docker",
				Command: "printf 'docker ok\\n'",
				Reason:  "check docker",
			}},
		},
		{
			Summary: "Check compose.",
			Actions: []core.Action{{
				ID:      "check-compose",
				Type:    core.ActionRunShell,
				Title:   "Check Compose",
				Command: "printf 'compose ok\\n'",
				Reason:  "check compose",
			}},
		},
		{
			Summary: "Everything is installed.",
			Actions: []core.Action{{
				ID:     "finish-install",
				Type:   core.ActionFinish,
				Title:  "Finish",
				Reason: "task complete",
			}},
		},
	}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Runtime.Config.Tools.SoftTurns = 1
	engine.Runtime.Config.Tools.MaxTurns = 4
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "inspect docker stack", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected continuation past soft-turn budget, got %d requests", len(provider.requests))
	}
	foundExtension := false
	for _, event := range renderer.narrativeEvents {
		if strings.Contains(event.Text, "Continuing past normal turn budget") {
			foundExtension = true
			break
		}
	}
	if !foundExtension {
		t.Fatalf("expected extension narration, got %#v", renderer.narrativeEvents)
	}
}

func TestRunTaskStopsOnRepeatedNoProgressAfterSoftTurns(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	provider := &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "Inspect docker.",
			Actions: []core.Action{{
				ID:      "inspect-docker-1",
				Type:    core.ActionRunShell,
				Title:   "Inspect Docker",
				Command: "printf 'docker ok\\n'",
				Reason:  "inspect docker",
			}},
		},
		{
			Summary: "Inspect docker again.",
			Actions: []core.Action{{
				ID:      "inspect-docker-2",
				Type:    core.ActionRunShell,
				Title:   "Inspect Docker",
				Command: "printf 'docker ok\\n'",
				Reason:  "inspect docker",
			}},
		},
		{
			Summary: "Inspect docker again.",
			Actions: []core.Action{{
				ID:      "inspect-docker-3",
				Type:    core.ActionRunShell,
				Title:   "Inspect Docker",
				Command: "printf 'docker ok\\n'",
				Reason:  "inspect docker",
			}},
		},
	}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Runtime.Config.Tools.SoftTurns = 1
	engine.Runtime.Config.Tools.MaxTurns = 5
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "inspect docker repeatedly", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	records, err := engine.Sessions.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 || records[0].CompletionStatus != core.CompletionIncompleteNoProgress {
		t.Fatalf("expected incomplete no-progress status, got %#v", records)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected no-progress stop before hard cap, got %d requests", len(provider.requests))
	}
}

func TestExecuteTurnUsesModelAuthoredActionHint(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Runtime.Config.UI.NarratedProgress = true
	response := core.LLMResponse{
		Summary: "Inspecting tests.",
		Narration: &core.Narration{
			ActionHints: []core.NarrationActionHint{{
				ActionID: "read-tests",
				Text:     "Let me read the current tests first.",
			}},
		},
		Actions: []core.Action{{
			ID:      "read-tests",
			Type:    core.ActionRunShell,
			Title:   "pwd",
			Command: "pwd",
			Reason:  "inspect current location",
		}},
	}

	if _, _, err := engine.executeTurnWithNarrator(context.Background(), response, newTurnNarrator(5, response.Narration)); err != nil {
		t.Fatalf("executeTurnWithNarrator() error = %v", err)
	}
	foundHint := false
	for _, event := range renderer.narrativeEvents {
		if event.Kind == core.NarrativeActionStarted && strings.Contains(event.Text, "read the current tests first") {
			foundHint = true
			break
		}
	}
	if !foundHint {
		t.Fatalf("expected model-authored action hint in events %#v", renderer.narrativeEvents)
	}
}

func TestExecuteTurnSynthesizesNarrationWithoutModelHints(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Runtime.Config.UI.NarratedProgress = true
	response := core.LLMResponse{
		Summary: "Inspecting files.",
		Actions: []core.Action{
			{ID: "1", Type: core.ActionRunShell, Title: "pwd", Command: "pwd", Reason: "inspect cwd"},
			{ID: "2", Type: core.ActionRunShell, Title: "ls", Command: "ls", Reason: "inspect files"},
		},
	}

	narrator := newTurnNarrator(7, nil)
	if _, _, err := engine.executeTurnWithNarrator(context.Background(), response, narrator); err != nil {
		t.Fatalf("executeTurnWithNarrator() error = %v", err)
	}
	started := []core.NarrativeEvent{}
	for _, event := range renderer.narrativeEvents {
		if event.Kind == core.NarrativeActionStarted {
			started = append(started, event)
		}
	}
	if len(started) != 2 {
		t.Fatalf("expected two action_started events, got %#v", renderer.narrativeEvents)
	}
	if !strings.Contains(started[0].Text, "pwd") || !strings.Contains(started[1].Text, "ls") {
		t.Fatalf("expected synthesized text to reference action targets, got %#v", started)
	}
	if started[0].Text == started[1].Text {
		t.Fatalf("expected varied synthesized narration, got %#v", started)
	}
}

func TestExecuteTurnNarratesBlockedInput(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{
		inputResults: []core.ActionResult{{
			InputKind:    core.InputText,
			FieldKey:     "token",
			ResponseMode: core.InputResponseSubmitted,
			InputValue:   "abc",
		}},
	}
	engine.Renderer = renderer
	engine.Runtime.Config.UI.NarratedProgress = true
	response := core.LLMResponse{
		Summary: "Need input.",
		Actions: []core.Action{{
			ID:           "ask-1",
			Type:         core.ActionAskUser,
			InputKind:    core.InputText,
			FieldKey:     "token",
			Prompt:       "Paste the token",
			Reason:       "I need your input before I can continue.",
			Alternatives: []string{"submit", "skip", "follow_up"},
		}},
	}

	if _, _, err := engine.executeTurnWithNarrator(context.Background(), response, newTurnNarrator(11, nil)); err != nil {
		t.Fatalf("executeTurnWithNarrator() error = %v", err)
	}
	if len(renderer.narrativeEvents) == 0 || renderer.narrativeEvents[0].Kind != core.NarrativeBlocked {
		t.Fatalf("expected blocked event before collecting input, got %#v", renderer.narrativeEvents)
	}
}

func TestRunChatDoesNotRepeatTurnStartedDuringExplicitInspectRetry(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	engine.Runtime.Config.UI.NarratedProgress = true
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "I need a wider view.",
			Actions: []core.Action{{
				ID:     "inspect-host",
				Type:   core.ActionInspect,
				Title:  "Inspect host",
				Reason: "collect broader context first",
			}},
		},
		{
			Summary: "Done.",
		},
	}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := &sessions.Record{}
	var history []core.ConversationMessage

	if err := engine.runChatPrompt(context.Background(), "inspect host", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}

	turnStarted := 0
	contextShift := 0
	for _, event := range renderer.narrativeEvents {
		if event.Kind == core.NarrativeTurnStarted {
			turnStarted++
		}
		if event.Kind == core.NarrativeContextShift {
			contextShift++
		}
	}
	if contextShift == 0 {
		t.Fatalf("expected context shift event, got %#v", renderer.narrativeEvents)
	}
	if turnStarted != 1 {
		t.Fatalf("expected one turn-start event before context retry, got %d (%#v)", turnStarted, renderer.narrativeEvents)
	}
}

func TestRunTaskRepromptsWhenModelClaimsNextStepButReturnsNoActions(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	provider := &fakeProvider{responses: []core.LLMResponse{
		{
			Summary:  "Proceeding to inspect the host for any OpenClaw installation.",
			Findings: []string{"Will look for installed binaries and related containers."},
		},
		{
			Summary: "Inspecting host.",
			Actions: []core.Action{{
				ID:      "inspect-host",
				Type:    core.ActionRunShell,
				Title:   "List containers",
				Command: "pwd",
				Reason:  "perform the promised inspection step",
			}},
		},
	}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Runtime.Config.Tools.MaxTurns = 2
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "inspect openclaw", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected minimal-context retry to continue execution, got %d provider requests", len(provider.requests))
	}
	lastMessages := provider.requests[1].Messages
	if len(lastMessages) == 0 {
		t.Fatalf("expected second provider request messages, got %#v", provider.requests[1])
	}
	last := lastMessages[len(lastMessages)-1].Content
	if !strings.Contains(last, "returned no executable actions") {
		t.Fatalf("expected second request to ask for a concrete next action, got %q", last)
	}
}

func TestRunTaskExecuteFirstRunsActionWithoutFullContextRetry(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	provider := &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "Checking Docker.",
			Actions: []core.Action{{
				ID:      "check-docker",
				Type:    core.ActionRunShell,
				Title:   "Check Docker",
				Command: "printf 'docker ok\\n'",
				Reason:  "check whether Docker is installed",
			}},
		},
		{
			Summary: "Docker is installed.",
			Actions: []core.Action{{
				ID:     "finish-docker-task",
				Type:   core.ActionFinish,
				Title:  "Finish",
				Reason: "task complete",
			}},
		},
	}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "is docker installed?", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if len(provider.requests) > 2 {
		t.Fatalf("expected execute-first to avoid a pre-action full-context retry, got %d provider requests", len(provider.requests))
	}
}

func TestRunChatPromptCollectsFullContextForExplicitInspectAction(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{}
	engine.Renderer = renderer
	provider := &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "I need a wider view.",
			Actions: []core.Action{{
				ID:     "inspect-1",
				Type:   core.ActionInspect,
				Title:  "Inspect workspace",
				Reason: "collect broader context first",
			}},
		},
		{
			Summary: "Done.",
		},
	}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	record := &sessions.Record{}
	var history []core.ConversationMessage
	if err := engine.runChatPrompt(context.Background(), "inspect workspace", nil, &history, record, nil, threads.ThreadRef{}); err != nil {
		t.Fatalf("runChatPrompt() error = %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected explicit inspect action to trigger full-context retry, got %d provider requests", len(provider.requests))
	}
	foundContextShift := false
	for _, event := range renderer.narrativeEvents {
		if event.Kind == core.NarrativeContextShift {
			foundContextShift = true
			break
		}
	}
	if !foundContextShift {
		t.Fatalf("expected context shift event for explicit inspect action, got %#v", renderer.narrativeEvents)
	}
}

func TestRunChatRepromptsWhenModelClaimsNextStepButReturnsNoActions(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{prompts: []string{"inspect openclaw", "quit"}}
	engine.Renderer = renderer
	provider := &fakeProvider{responses: []core.LLMResponse{
		{
			Summary:  "Proceeding to inspect the host for any OpenClaw installation.",
			Findings: []string{"Will look for installed binaries and related containers."},
		},
		{
			Summary: "Inspecting host.",
			Actions: []core.Action{{
				ID:      "inspect-host",
				Type:    core.ActionRunShell,
				Title:   "List containers",
				Command: "pwd",
				Reason:  "perform the promised inspection step",
			}},
		},
	}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Runtime.Config.Tools.MaxTurns = 2
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunChat(context.Background(), "", nil); err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected chat recovery reprompt, got %d provider requests", len(provider.requests))
	}
	found := false
	for _, req := range provider.requests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "returned no executable actions") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected missing action continuation message, got %#v", provider.requests)
	}
}

func TestRunChatModeCommandUpdatesInteraction(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{prompts: []string{"/mode plan-first", "quit"}}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{Summary: "Done"}}}
	engine.Runtime.NoContext = true
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunChat(context.Background(), "", nil); err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if engine.Runtime.Interaction != core.InteractionPlanFirst {
		t.Fatalf("expected plan-first interaction, got %q", engine.Runtime.Interaction)
	}
	if renderer.lastInteraction != core.InteractionPlanFirst {
		t.Fatalf("expected renderer interaction update, got %q", renderer.lastInteraction)
	}
}

func TestRunChatApprovalCommandUpdatesApprovalMode(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{prompts: []string{"/approval agent", "quit"}}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{Summary: "Done"}}}
	engine.Runtime.NoContext = true
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunChat(context.Background(), "", nil); err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if engine.Runtime.ApprovalMode != core.ApprovalAgent {
		t.Fatalf("expected approval mode agent, got %q", engine.Runtime.ApprovalMode)
	}
	if renderer.lastApproval != core.ApprovalAgent {
		t.Fatalf("expected renderer approval update, got %q", renderer.lastApproval)
	}
}

func TestRunChatClearCommandClearsRendererScreen(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{prompts: []string{"/clear", "quit"}}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{Summary: "Done"}}}
	engine.Runtime.NoContext = true
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunChat(context.Background(), "", nil); err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if renderer.clearCalls != 1 {
		t.Fatalf("expected clear screen call, got %d", renderer.clearCalls)
	}
}

func TestRunChatContinuesAfterProviderError(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{prompts: []string{"check docker", "quit"}}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{
		errs: []error{errors.New("provider request failed: context deadline exceeded")},
	}
	engine.Runtime.NoContext = true
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunChat(context.Background(), "", nil); err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if len(renderer.messages) == 0 {
		t.Fatalf("expected provider error message to be rendered")
	}
	if !strings.Contains(renderer.messages[0], "provider request failed") {
		t.Fatalf("expected provider error message, got %#v", renderer.messages)
	}
}

func TestRunChatExitsCleanlyWhenPromptInputCloses(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{
		prompts:   []string{"check docker"},
		promptErr: os.ErrClosed,
	}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{Summary: "Done"}}}
	engine.Runtime.NoContext = true
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunChat(context.Background(), "", nil); err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
}

func TestRunTaskResumesAfterStructuredUserInput(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{
		inputResults: []core.ActionResult{{
			InputKind:    core.InputText,
			FieldKey:     "bot_token",
			ResponseMode: core.InputResponseSubmitted,
			InputValue:   "123:abc",
		}},
	}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "Need a token.",
			Actions: []core.Action{{
				ID:              "ask-1",
				Type:            core.ActionAskUser,
				InputKind:       core.InputText,
				FieldKey:        "bot_token",
				Prompt:          "Paste the bot token",
				Alternatives:    []string{"submit", "skip", "follow_up"},
				DestinationHint: "IronLark will export it for the next command",
			}},
		},
		{Summary: "Configured."},
	}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "set up the bot", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if len(renderer.blockerActions) != 1 {
		t.Fatalf("expected one blocker action, got %d", len(renderer.blockerActions))
	}
	records, err := engine.Sessions.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one session record, got %d", len(records))
	}
	if len(records[0].Results) == 0 || records[0].Results[0].InputValue != "123:abc" {
		t.Fatalf("expected non-secret input to persist in record results, got %#v", records[0].Results)
	}
}

func TestRunChatDoesNotOpenStructuredInputForPlainClarification(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{prompts: []string{"quit"}}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{{
		Summary: "You sent \"1\"; what command should I run next?",
		Actions: []core.Action{{
			ID:           "clarify-1",
			Type:         core.ActionAskUser,
			InputKind:    core.InputText,
			FieldKey:     "next_task",
			Prompt:       "What should I do next?",
			Alternatives: []string{"submit", "skip", "follow_up"},
			ExpectsValue: false,
		}},
	}}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunChat(context.Background(), "1", nil); err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	if len(renderer.blockerActions) != 0 {
		t.Fatalf("expected plain clarification to stay in chat, got blocker actions %#v", renderer.blockerActions)
	}
	if len(renderer.responses) == 0 || !strings.Contains(renderer.responses[0].Summary, "what command should I run next") {
		t.Fatalf("expected conversational clarification response, got %#v", renderer.responses)
	}
}

func TestRunTaskUsesStructuredInputForConcreteTextValue(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{
		inputResults: []core.ActionResult{{
			InputKind:    core.InputText,
			FieldKey:     "service_name",
			ResponseMode: core.InputResponseSubmitted,
			InputValue:   "nginx",
		}},
	}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "I need the service name.",
			Actions: []core.Action{{
				ID:              "ask-structured",
				Type:            core.ActionAskUser,
				InputKind:       core.InputText,
				FieldKey:        "service_name",
				Prompt:          "Which service should I inspect?",
				DestinationHint: "IronLark will use it in the next command.",
				ExpectsValue:    true,
				Alternatives:    []string{"submit", "skip", "follow_up"},
			}},
		},
		{Summary: "Checked the service."},
	}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "inspect a service", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if len(renderer.blockerActions) != 1 {
		t.Fatalf("expected structured text input to open blocker UI, got %d", len(renderer.blockerActions))
	}
}

func TestRunTaskRedactsSecretUserInputInSessionStorage(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{
		inputResults: []core.ActionResult{{
			InputKind:    core.InputSecret,
			FieldKey:     "api_key",
			ResponseMode: core.InputResponseSubmitted,
			InputValue:   "secret-token",
			IsSensitive:  true,
		}},
	}
	engine.Renderer = renderer
	engine.Provider = &fakeProvider{responses: []core.LLMResponse{
		{
			Summary: "Need an API key.",
			Actions: []core.Action{{
				ID:           "ask-secret",
				Type:         core.ActionAskUser,
				InputKind:    core.InputSecret,
				FieldKey:     "api_key",
				Prompt:       "Paste the API key",
				Alternatives: []string{"submit", "skip", "follow_up"},
			}},
		},
		{Summary: "Configured."},
	}}
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "configure service", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	records, err := engine.Sessions.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one session record, got %d", len(records))
	}
	if got := records[0].Results[0].InputValue; got != "[secret]" {
		t.Fatalf("expected redacted secret in results, got %q", got)
	}
	data, err := os.ReadFile(filepath.Join(engine.Sessions.Dir, records[0].ID+".json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(data), "secret-token") {
		t.Fatalf("expected session file to redact secret, got %s", string(data))
	}
}

func TestRunTaskInjectsGraphDigestIntoPrompt(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	provider := &fakeProvider{responses: []core.LLMResponse{{Summary: "Done"}}}
	engine.Provider = provider
	engine.Runtime.NoContext = true
	engine.Collector = ctxpkg.New(redact.New(nil))
	engine.Sessions = sessions.Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := os.MkdirAll(engine.Sessions.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	graphDir := filepath.Join(t.TempDir(), "graph")
	engine.Graph = graph.NewManager(graph.Store{Dir: graphDir}, cfgpkg.DefaultConfig().Graph, engine.Runtime.WorkingDir)
	engine.Graph.Host = "srv-1"
	engine.Graph.User = "richardson"
	if err := engine.Graph.Store.SaveSnapshot(graph.GraphSnapshot{
		ID:          "snap-1",
		Host:        engine.Graph.HostKey(),
		CollectedAt: time.Now().UTC(),
		Services:    []graph.Service{{Name: "nextjs.service", ActiveState: "active"}},
		Listeners:   []graph.Listener{{Port: 3000, Proto: "tcp"}},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), "is nextjs running?", nil, "oneshot"); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected provider request")
	}
	if !strings.Contains(provider.requests[0].Messages[0].Content, "Server graph memory:") {
		t.Fatalf("expected graph digest in prompt, got %q", provider.requests[0].Messages[0].Content)
	}
	if !strings.Contains(provider.requests[0].Messages[0].Content, "nextjs.service") {
		t.Fatalf("expected graph service in prompt, got %q", provider.requests[0].Messages[0].Content)
	}
}

func TestVerificationRespectsApprovalFlow(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{approvalChoices: []core.ApprovalDecision{{Kind: core.ApprovalDecisionAllowOnce}}}
	engine.Renderer = renderer
	response := core.LLMResponse{
		Summary: "Verify service.",
		Verification: []core.Verification{{
			Type:        core.ActionRunShell,
			Command:     "curl -I http://127.0.0.1:3000",
			SuccessHint: "verify HTTP is reachable",
		}},
	}

	results, _, err := engine.executeTurn(context.Background(), response)
	if err != nil {
		t.Fatalf("executeTurn() error = %v", err)
	}
	if renderer.approvalPromptCalls == 0 {
		t.Fatal("expected verification to request approval")
	}
	if len(results) != 1 {
		t.Fatalf("expected one verification result, got %d", len(results))
	}
	if results[0].Summary == "blocked by read-only mode" {
		t.Fatalf("expected verification to stop using forced read-only path, got %#v", results[0])
	}
}

func TestVerificationCanBeDeniedByUser(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{approvalChoices: []core.ApprovalDecision{{Kind: core.ApprovalDecisionDenyOnce}}}
	engine.Renderer = renderer
	response := core.LLMResponse{
		Summary: "Verify service.",
		Verification: []core.Verification{{
			Type:        core.ActionRunShell,
			Command:     "curl -I http://127.0.0.1:3000",
			SuccessHint: "verify HTTP is reachable",
		}},
	}

	results, _, err := engine.executeTurn(context.Background(), response)
	if err != nil {
		t.Fatalf("executeTurn() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one verification result, got %d", len(results))
	}
	if !results[0].Skipped || results[0].Summary != "user declined action" {
		t.Fatalf("expected declined verification result, got %#v", results[0])
	}
}

func TestPromptAutoAcceptPersistsThreshold(t *testing.T) {
	engine, _ := testEngine(t, core.InteractionExecuteFirst)
	renderer := &trackingRenderer{approvalChoices: []core.ApprovalDecision{{Kind: core.ApprovalDecisionAutoAccept, AutoAcceptThrough: core.RiskMedium}}}
	engine.Renderer = renderer

	response := core.LLMResponse{
		Summary: "Restart service.",
		Actions: []core.Action{{
			ID:      "1",
			Type:    core.ActionRunShell,
			Title:   "restart",
			Command: "systemctl restart nginx",
		}},
	}

	results, _, err := engine.executeTurn(context.Background(), response)
	if err != nil {
		t.Fatalf("executeTurn() error = %v", err)
	}
	if len(results) != 1 || results[0].Skipped {
		t.Fatalf("expected action to run after auto accept, got %#v", results)
	}
	level, ok, err := engine.PolicyStore.AutoAcceptThrough()
	if err != nil {
		t.Fatalf("AutoAcceptThrough() error = %v", err)
	}
	if !ok || level != core.RiskMedium {
		t.Fatalf("expected persisted medium threshold, got %q %t", level, ok)
	}
}

func testEngine(t *testing.T, interaction core.InteractionMode) (*Engine, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	out := &bytes.Buffer{}
	policyPath := filepath.Join(root, "policy.json")
	runtime := state.Runtime{
		Config: cfgpkg.DefaultConfig(),
		Paths: cfgpkg.Paths{
			PolicyPath:     policyPath,
			PatchesDir:     filepath.Join(root, "patches"),
			CheckpointsDir: filepath.Join(root, "checkpoints"),
		},
		WorkingDir:  root,
		Interaction: interaction,
	}
	renderer := render.New(strings.NewReader(""), out, out, false, "never")
	classifier := policypkg.NewClassifier(runtime.Config.Security.ProtectedPaths)
	exec := &executor.Executor{
		WorkingDir:     root,
		MaxOutputBytes: runtime.Config.Context.MaxCommandOutputBytes,
		MaxListEntries: runtime.Config.Context.MaxListEntries,
		MaxFileBytes:   runtime.Config.Context.MaxFileBytes,
		Redactor:       redact.New(nil),
		Classifier:     classifier,
		PatchStore:     patches.Store{Dir: runtime.Paths.PatchesDir},
		CheckpointStore: checkpoints.Store{
			Dir: runtime.Paths.CheckpointsDir,
		},
		Searcher: search.Searcher{UserAgent: "lark-term/test"},
	}
	if err := os.MkdirAll(runtime.Paths.PatchesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtime.Paths.CheckpointsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Engine{
		Runtime:     runtime,
		Executor:    exec,
		Renderer:    renderer,
		PolicyStore: policypkg.Store{Path: policyPath},
	}, out
}

type fakeProvider struct {
	responses []core.LLMResponse
	errs      []error
	index     int
	requests  []provider.Request
}

func (f *fakeProvider) Generate(ctx context.Context, req provider.Request) (core.LLMResponse, error) {
	reqCopy := req
	reqCopy.Messages = append([]core.ConversationMessage(nil), req.Messages...)
	f.requests = append(f.requests, reqCopy)
	if len(f.errs) > 0 {
		err := f.errs[0]
		if len(f.errs) > 1 {
			f.errs = f.errs[1:]
		}
		if err != nil {
			return core.LLMResponse{}, err
		}
	}
	if len(f.responses) == 0 {
		return core.LLMResponse{}, nil
	}
	if f.index >= len(f.responses) {
		return f.responses[len(f.responses)-1], nil
	}
	resp := f.responses[f.index]
	f.index++
	return resp, nil
}

func (f *fakeProvider) WebSearch(ctx context.Context, req provider.SearchRequest) ([]string, error) {
	return nil, provider.ErrWebSearchUnsupported
}

type trackingRenderer struct {
	beginCalls          int
	endCalls            int
	clearCalls          int
	approvalPromptCalls int
	narrativeEvents     []core.NarrativeEvent
	responses           []core.LLMResponse
	streamChunks        []core.ActionOutputChunk
	lastApproval        core.ApprovalMode
	lastInteraction     core.InteractionMode
	prompts             []string
	approvalChoices     []core.ApprovalDecision
	inputResults        []core.ActionResult
	blockerActions      []core.Action
	messages            []string
	promptErr           error
	index               int
}

func (r *trackingRenderer) Snapshot(snapshot ctxpkg.Snapshot) error { return nil }
func (r *trackingRenderer) Response(response core.LLMResponse) error {
	r.responses = append(r.responses, response)
	return nil
}
func (r *trackingRenderer) PlannedActions(actions []core.Action, previews []core.RiskReport) {}
func (r *trackingRenderer) ActionProgress(action core.Action)                                {}
func (r *trackingRenderer) StreamActionOutput(action core.Action, chunk core.ActionOutputChunk) {
	r.streamChunks = append(r.streamChunks, chunk)
}
func (r *trackingRenderer) ApprovalPrompt(action core.Action, report core.RiskReport) {
	r.approvalPromptCalls++
}
func (r *trackingRenderer) Result(result core.ActionResult) {}
func (r *trackingRenderer) Narrate(event core.NarrativeEvent) {
	r.narrativeEvents = append(r.narrativeEvents, event)
}
func (r *trackingRenderer) BeginThinking(label string)                               { r.beginCalls++ }
func (r *trackingRenderer) EndThinking()                                             { r.endCalls++ }
func (r *trackingRenderer) SetInteraction(mode core.InteractionMode)                 { r.lastInteraction = mode }
func (r *trackingRenderer) SetApproval(mode core.ApprovalMode)                       { r.lastApproval = mode }
func (r *trackingRenderer) SetModelContext(provider, model string, options []string) {}
func (r *trackingRenderer) SetOpsSummary(summary string)                             {}
func (r *trackingRenderer) SetSecretVisibility(visible bool)                         {}
func (r *trackingRenderer) SecretVisibility() string                                 { return "visible" }
func (r *trackingRenderer) ClearScreen()                                             { r.clearCalls++ }
func (r *trackingRenderer) Sessions(records []sessions.Record) error                 { return nil }
func (r *trackingRenderer) Patches(records []patches.Record) error                   { return nil }
func (r *trackingRenderer) Checkpoints(records []checkpoints.Record) error           { return nil }
func (r *trackingRenderer) PromptChoice() (string, error)                            { return "1", nil }
func (r *trackingRenderer) PromptApprovalChoice(current core.RiskLevel) (core.ApprovalDecision, error) {
	if len(r.approvalChoices) == 0 {
		return core.ApprovalDecision{Kind: core.ApprovalDecisionAllowOnce}, nil
	}
	choice := r.approvalChoices[0]
	if len(r.approvalChoices) > 1 {
		r.approvalChoices = r.approvalChoices[1:]
	}
	return choice, nil
}
func (r *trackingRenderer) CollectUserInput(action core.Action) (core.ActionResult, error) {
	r.blockerActions = append(r.blockerActions, action)
	result := r.inputResults[0]
	r.inputResults = r.inputResults[1:]
	return result, nil
}
func (r *trackingRenderer) Confirm(label string, double bool) (bool, error) { return true, nil }
func (r *trackingRenderer) ReadPrompt(prefix string) (string, error) {
	if r.index >= len(r.prompts) {
		if r.promptErr != nil {
			return "", r.promptErr
		}
		return "quit", nil
	}
	p := r.prompts[r.index]
	r.index++
	return p, nil
}
func (r *trackingRenderer) Message(text string)     { r.messages = append(r.messages, text) }
func (r *trackingRenderer) MessageJSON(v any) error { return nil }
