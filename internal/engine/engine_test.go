package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/patches"
	policypkg "github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/search"
	"github.com/richardsondx/IronLark/internal/sessions"
	"github.com/richardsondx/IronLark/internal/state"
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
	if !engine.actionNeedsApproval(action, report, policypkg.Match{}) {
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
	if engine.actionNeedsApproval(action, report, policypkg.Match{Matched: true, Decision: policypkg.DecisionAllow}) {
		t.Fatalf("expected allow rule to suppress approval")
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
	index     int
}

func (f *fakeProvider) Generate(ctx context.Context, req provider.Request) (core.LLMResponse, error) {
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

type trackingRenderer struct {
	beginCalls      int
	endCalls        int
	clearCalls      int
	lastApproval    core.ApprovalMode
	lastInteraction core.InteractionMode
	prompts         []string
	inputResults    []core.ActionResult
	blockerActions  []core.Action
	index           int
}

func (r *trackingRenderer) Snapshot(snapshot ctxpkg.Snapshot) error                          { return nil }
func (r *trackingRenderer) Response(response core.LLMResponse) error                         { return nil }
func (r *trackingRenderer) PlannedActions(actions []core.Action, previews []core.RiskReport) {}
func (r *trackingRenderer) ActionProgress(action core.Action)                                {}
func (r *trackingRenderer) ApprovalPrompt(action core.Action, report core.RiskReport)        {}
func (r *trackingRenderer) Result(result core.ActionResult)                                  {}
func (r *trackingRenderer) BeginThinking(label string)                                       { r.beginCalls++ }
func (r *trackingRenderer) EndThinking()                                                     { r.endCalls++ }
func (r *trackingRenderer) SetInteraction(mode core.InteractionMode)                         { r.lastInteraction = mode }
func (r *trackingRenderer) SetApproval(mode core.ApprovalMode)                               { r.lastApproval = mode }
func (r *trackingRenderer) SetSecretVisibility(visible bool)                                 {}
func (r *trackingRenderer) SecretVisibility() string                                         { return "visible" }
func (r *trackingRenderer) ClearScreen()                                                     { r.clearCalls++ }
func (r *trackingRenderer) Sessions(records []sessions.Record) error                         { return nil }
func (r *trackingRenderer) Patches(records []patches.Record) error                           { return nil }
func (r *trackingRenderer) Checkpoints(records []checkpoints.Record) error                   { return nil }
func (r *trackingRenderer) PromptChoice() (string, error)                                    { return "1", nil }
func (r *trackingRenderer) PromptApprovalChoice() (string, error)                            { return "1", nil }
func (r *trackingRenderer) CollectUserInput(action core.Action) (core.ActionResult, error) {
	r.blockerActions = append(r.blockerActions, action)
	result := r.inputResults[0]
	r.inputResults = r.inputResults[1:]
	return result, nil
}
func (r *trackingRenderer) Confirm(label string, double bool) (bool, error) { return true, nil }
func (r *trackingRenderer) ReadPrompt(prefix string) (string, error) {
	p := r.prompts[r.index]
	r.index++
	return p, nil
}
func (r *trackingRenderer) Message(text string)     {}
func (r *trackingRenderer) MessageJSON(v any) error { return nil }
