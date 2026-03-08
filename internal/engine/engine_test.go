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
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/patches"
	policypkg "github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/search"
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
