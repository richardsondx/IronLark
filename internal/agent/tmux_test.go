package agent

import (
	"context"
	"strings"
	"testing"
)

type fakeRunner struct {
	runCalls    [][]string
	outputCalls [][]string
	failTargets map[string]bool
	outputs     map[string]string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) error {
	call := append([]string{name}, args...)
	f.runCalls = append(f.runCalls, call)
	if len(args) >= 1 && args[0] == "has-session" && f.failTargets[args[len(args)-1]] {
		return context.Canceled
	}
	if len(args) >= 1 && args[0] == "list-panes" && f.failTargets[args[len(args)-1]] {
		return context.Canceled
	}
	return nil
}

func (f *fakeRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.outputCalls = append(f.outputCalls, call)
	return []byte(f.outputs[strings.Join(args, " ")]), nil
}

func TestEnsureWorkspaceCreatesDetachedSessionOutsideTmux(t *testing.T) {
	runner := &fakeRunner{failTargets: map[string]bool{"ironlark-abc": true, "ironlark-abc:agent-app": true}}
	manager := TmuxManager{Runner: runner}
	workspace := Workspace{TmuxSession: "ironlark-abc", TmuxWindow: "agent-app", TmuxTarget: "ironlark-abc:agent-app", CWD: "/opt/app"}

	if err := manager.EnsureWorkspace(context.Background(), workspace, "lk agent __agent-ui", false); err != nil {
		t.Fatalf("ensure workspace returned error: %v", err)
	}
	last := runner.runCalls[len(runner.runCalls)-1]
	if len(last) < 3 || last[1] != "new-session" {
		t.Fatalf("expected new-session call, got %#v", last)
	}
}

func TestEnsureWorkspaceCreatesWindowInsideTmux(t *testing.T) {
	runner := &fakeRunner{failTargets: map[string]bool{"team:agent-app": true}}
	manager := TmuxManager{Runner: runner}
	workspace := Workspace{TmuxSession: "team", TmuxWindow: "agent-app", TmuxTarget: "team:agent-app", CWD: "/opt/app"}

	if err := manager.EnsureWorkspace(context.Background(), workspace, "lk agent __agent-ui", true); err != nil {
		t.Fatalf("ensure workspace returned error: %v", err)
	}
	last := runner.runCalls[len(runner.runCalls)-1]
	if len(last) < 3 || last[1] != "new-window" {
		t.Fatalf("expected new-window call, got %#v", last)
	}
}

func TestCurrentSessionUsesDisplayMessage(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{"display-message -p #S": "ops\n"}}
	manager := TmuxManager{Runner: runner}

	session, err := manager.CurrentSession(context.Background())
	if err != nil {
		t.Fatalf("current session returned error: %v", err)
	}
	if session != "ops" {
		t.Fatalf("expected session ops, got %q", session)
	}
}
