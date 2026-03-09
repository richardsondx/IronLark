package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/richardsondx/IronLark/internal/app"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/state"
	"github.com/richardsondx/IronLark/internal/threads"
)

func TestContextStatusAndClear(t *testing.T) {
	temp := t.TempDir()
	project := filepath.Join(temp, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(temp, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(temp, ".local", "share"))
	t.Chdir(project)

	application, err := app.New(state.Overrides{})
	if err != nil {
		t.Fatalf("app.New() error = %v", err)
	}
	ref, err := threads.ResolveDefaultThread(application.Runtime)
	if err != nil {
		t.Fatalf("ResolveDefaultThread() error = %v", err)
	}
	thread := threads.NewThread(ref)
	thread.Turns = []threads.ThreadTurn{{
		UserPrompt:       "did you fix it?",
		AssistantSummary: "asked for clarification",
		CreatedAt:        time.Now().UTC(),
	}}
	thread.RollingSummary = "Earlier the assistant lacked thread context."
	if err := application.Threads.Save(thread); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	statusOutput := captureStdout(t, func() error {
		cmd := NewRootCommand()
		cmd.SetArgs([]string{"context", "--json"})
		return cmd.Execute()
	})
	if !strings.Contains(statusOutput, ref.ThreadID) {
		t.Fatalf("expected context status to mention thread id, got %q", statusOutput)
	}

	if err := runCommand([]string{"context", "clear"}); err != nil {
		t.Fatalf("context clear error = %v", err)
	}
	cleared, err := application.Threads.Load(ref.ThreadID)
	if err != nil {
		t.Fatalf("Load() after clear error = %v", err)
	}
	if len(cleared.Turns) != 0 {
		t.Fatalf("expected cleared turns, got %d", len(cleared.Turns))
	}
}

func TestContextUseAndDrop(t *testing.T) {
	temp := t.TempDir()
	project := filepath.Join(temp, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(temp, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(temp, ".local", "share"))
	t.Chdir(project)

	application, err := app.New(state.Overrides{})
	if err != nil {
		t.Fatalf("app.New() error = %v", err)
	}
	ref, err := threads.ResolveDefaultThread(application.Runtime)
	if err != nil {
		t.Fatalf("ResolveDefaultThread() error = %v", err)
	}
	thread := threads.NewThread(ref)
	if err := application.Threads.Save(thread); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := runCommand([]string{"context", "use", "manual-thread"}); err != nil {
		t.Fatalf("context use error = %v", err)
	}
	overrideOutput := captureStdout(t, func() error {
		cmd := NewRootCommand()
		cmd.SetArgs([]string{"context", "--json"})
		return cmd.Execute()
	})
	if !strings.Contains(overrideOutput, "manual-thread") {
		t.Fatalf("expected override thread in status output, got %q", overrideOutput)
	}

	if err := runCommand([]string{"context", "drop", "--thread", ref.ThreadID}); err != nil {
		t.Fatalf("context drop error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(application.Runtime.Paths.ThreadsDir, ref.ThreadID+".json")); !os.IsNotExist(err) {
		t.Fatalf("expected dropped thread removed, err=%v", err)
	}
}

func TestPlanFlagSelectsPlanFirstInteraction(t *testing.T) {
	temp := t.TempDir()
	project := filepath.Join(temp, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(temp, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(temp, ".local", "share"))
	t.Chdir(project)

	application, err := buildApp(&rootFlags{plan: true})
	if err != nil {
		t.Fatalf("buildApp() error = %v", err)
	}
	if application.Runtime.Interaction != core.InteractionPlanFirst {
		t.Fatalf("expected plan-first interaction, got %q", application.Runtime.Interaction)
	}
}

func TestModelCommandShowsCurrentAndOptions(t *testing.T) {
	temp := t.TempDir()
	project := filepath.Join(temp, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(temp, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(temp, ".local", "share"))
	t.Chdir(project)

	output := captureStdout(t, func() error {
		cmd := NewRootCommand()
		cmd.SetArgs([]string{"model"})
		return cmd.Execute()
	})

	for _, fragment := range []string{
		"Current model: gpt-4.1-mini",
		"Available model options:",
		"openai: gpt-4.1-mini, gpt-5-mini",
		"openrouter: openai/gpt-4.1-mini",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("expected %q in output, got %q", fragment, output)
		}
	}
}

func TestPolicyCommands(t *testing.T) {
	temp := t.TempDir()
	project := filepath.Join(temp, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(temp, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(temp, ".local", "share"))
	t.Chdir(project)

	if err := runCommand([]string{"policy", "allow", "command", "systemctl status"}); err != nil {
		t.Fatalf("policy allow error = %v", err)
	}
	listOutput := captureStdout(t, func() error {
		cmd := NewRootCommand()
		cmd.SetArgs([]string{"policy", "list"})
		return cmd.Execute()
	})
	if !strings.Contains(listOutput, "systemctl status") {
		t.Fatalf("expected policy rule in list output, got %q", listOutput)
	}
}

func runCommand(args []string) error {
	cmd := NewRootCommand()
	cmd.SetArgs(args)
	return cmd.Execute()
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	runErr := fn()
	_ = w.Close()
	if runErr != nil {
		t.Fatalf("command error: %v", runErr)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy stdout: %v", err)
	}
	return buf.String()
}
