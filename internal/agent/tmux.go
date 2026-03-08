package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

type TmuxManager struct {
	Runner Runner
}

func (m TmuxManager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return ExecRunner{}
}

func (m TmuxManager) Check(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is required for `lk agent`; install tmux on this machine or use `lk chat` instead")
	}
	_, err := m.runner().Output(ctx, "tmux", "-V")
	return err
}

func (m TmuxManager) HasSession(ctx context.Context, session string) bool {
	return m.runner().Run(ctx, "tmux", "has-session", "-t", session) == nil
}

func (m TmuxManager) HasTarget(ctx context.Context, target string) bool {
	return m.runner().Run(ctx, "tmux", "list-panes", "-t", target) == nil
}

func (m TmuxManager) CurrentSession(ctx context.Context) (string, error) {
	output, err := m.runner().Output(ctx, "tmux", "display-message", "-p", "#S")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (m TmuxManager) EnsureWorkspace(ctx context.Context, workspace Workspace, command string, insideTmux bool) error {
	if insideTmux {
		if m.HasTarget(ctx, workspace.TmuxTarget) {
			return nil
		}
		return m.runner().Run(ctx, "tmux", "new-window", "-t", workspace.TmuxSession, "-n", workspace.TmuxWindow, "-c", workspace.CWD, command)
	}
	if m.HasSession(ctx, workspace.TmuxSession) {
		if m.HasTarget(ctx, workspace.TmuxTarget) {
			return nil
		}
		return m.runner().Run(ctx, "tmux", "new-window", "-t", workspace.TmuxSession, "-n", workspace.TmuxWindow, "-c", workspace.CWD, command)
	}
	return m.runner().Run(ctx, "tmux", "new-session", "-d", "-s", workspace.TmuxSession, "-n", workspace.TmuxWindow, "-c", workspace.CWD, command)
}

func (m TmuxManager) Attach(ctx context.Context, workspace Workspace, insideTmux bool) error {
	if insideTmux {
		return m.runner().Run(ctx, "tmux", "switch-client", "-t", workspace.TmuxTarget)
	}
	return m.runner().Run(ctx, "tmux", "attach-session", "-t", workspace.TmuxSession)
}

func (m TmuxManager) Kill(ctx context.Context, workspace Workspace) error {
	if workspace.CreatedInsideTmux {
		return m.runner().Run(ctx, "tmux", "kill-window", "-t", workspace.TmuxTarget)
	}
	return m.runner().Run(ctx, "tmux", "kill-session", "-t", workspace.TmuxSession)
}
