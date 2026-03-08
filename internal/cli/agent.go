package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/richardsondx/IronLark/internal/agent"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/threads"
)

func newAgentCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent [initial prompt]",
		Short: "Start or resume the SSH-first tmux agent workspace",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentWorkspace(cmd.Context(), flags, strings.Join(args, " "))
		},
	}
	cmd.AddCommand(newAgentAttachCommand(flags))
	cmd.AddCommand(newAgentListCommand(flags))
	cmd.AddCommand(newAgentStopCommand(flags))
	cmd.AddCommand(newAgentUICommand(flags))
	return cmd
}

func newAgentAttachCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <workspace>",
		Short: "Attach to an existing agent workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			workspace, err := resolveWorkspace(application.Agents, args[0])
			if err != nil {
				return err
			}
			tmux := agent.TmuxManager{}
			if err := tmux.Check(cmd.Context()); err != nil {
				return err
			}
			return tmux.Attach(cmd.Context(), workspace, insideTmux())
		},
	}
}

func newAgentListCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List resumable agent workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			workspaces, err := application.Agents.List()
			if err != nil {
				return err
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(workspaces)
			}
			for _, workspace := range workspaces {
				application.Renderer.Message(fmt.Sprintf("%s  %s  %s  %s  %s",
					workspace.Key,
					workspace.LastActiveAt.Format("2006-01-02 15:04:05"),
					workspace.Host,
					workspace.CWD,
					workspace.TmuxTarget,
				))
			}
			return nil
		},
	}
}

func newAgentStopCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <workspace>",
		Short: "Stop an agent workspace and kill its tmux target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			workspace, err := resolveWorkspace(application.Agents, args[0])
			if err != nil {
				return err
			}
			tmux := agent.TmuxManager{}
			if err := tmux.Check(cmd.Context()); err != nil {
				return err
			}
			if err := tmux.Kill(cmd.Context(), workspace); err != nil {
				return err
			}
			if err := application.Agents.Delete(workspace.Key); err != nil {
				return err
			}
			application.Renderer.Message("Stopped agent workspace " + workspace.Key)
			return nil
		},
	}
}

func newAgentUICommand(flags *rootFlags) *cobra.Command {
	var workspaceKey string
	var initialPrompt string
	cmd := &cobra.Command{
		Use:    "__agent-ui",
		Short:  "Internal agent UI runner",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			workspace, err := application.Agents.Load(workspaceKey)
			if err != nil {
				return err
			}
			if workspace.Key == "" {
				return fmt.Errorf("agent workspace %q was not found", workspaceKey)
			}
			if err := os.Chdir(workspace.CWD); err != nil {
				return err
			}
			application.Renderer = render.NewAgent(os.Stdin, os.Stdout, os.Stderr, flags.color, render.AgentMeta{
				Host:          workspace.Host,
				CWD:           workspace.CWD,
				Model:         application.Runtime.Model,
				ApprovalMode:  application.Runtime.ApprovalMode,
				ThreadID:      workspace.ThreadID,
				CompactAtRows: application.Runtime.Config.Agent.CompactModeRows,
				Interaction:   application.Runtime.Interaction,
				PolicyStore:   application.PolicyStore,
			})
			defer fmt.Fprint(os.Stdout, "\033[?1049l")
			application.Renderer.Message("Remote operator workspace ready.")
			if err := application.Agents.Save(workspace); err != nil {
				return err
			}
			return application.Engine().RunChat(cmd.Context(), initialPrompt, nil)
		},
	}
	cmd.Flags().StringVar(&workspaceKey, "workspace", "", "workspace key")
	cmd.Flags().StringVar(&initialPrompt, "prompt", "", "initial prompt")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}

func insideTmux() bool {
	return strings.TrimSpace(os.Getenv("TMUX")) != ""
}

func runAgentWorkspace(ctx context.Context, flags *rootFlags, initialPrompt string) error {
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	tmux := agent.TmuxManager{}
	if err := tmux.Check(ctx); err != nil {
		return err
	}
	userName, host := agent.CurrentIdentity()
	ref, err := threads.ResolveDefaultThread(application.Runtime)
	if err != nil {
		return err
	}
	currentInsideTmux := insideTmux()
	workspace := agent.BuildWorkspace(application.Runtime.Config.Agent.SessionPrefix, userName, host, application.Runtime.WorkingDir, ref.ThreadID, currentInsideTmux)
	if currentInsideTmux {
		sessionName, err := tmux.CurrentSession(ctx)
		if err != nil {
			return err
		}
		workspace.TmuxSession = sessionName
		workspace.TmuxTarget = sessionName + ":" + workspace.TmuxWindow
	}
	if existing, err := application.Agents.Load(workspace.Key); err == nil && existing.Key != "" {
		workspace = existing
	}
	if strings.TrimSpace(workspace.ThreadID) == "" {
		workspace.ThreadID = ref.ThreadID
	}

	command, err := agentUICommand(flags, workspace, initialPrompt)
	if err != nil {
		return err
	}
	if err := tmux.EnsureWorkspace(ctx, workspace, command, currentInsideTmux); err != nil {
		return err
	}
	if err := application.Agents.Save(workspace); err != nil {
		return err
	}
	return tmux.Attach(ctx, workspace, currentInsideTmux)
}

func agentUICommand(flags *rootFlags, workspace agent.Workspace, initialPrompt string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	args := []string{shellQuote(executable), "agent", "__agent-ui", "--workspace", shellQuote(workspace.Key), "--thread", shellQuote(workspace.ThreadID)}
	if strings.TrimSpace(flags.provider) != "" {
		args = append(args, "--provider", shellQuote(flags.provider))
	}
	if strings.TrimSpace(flags.model) != "" {
		args = append(args, "--model", shellQuote(flags.model))
	}
	if strings.TrimSpace(flags.profile) != "" {
		args = append(args, "--profile", shellQuote(flags.profile))
	}
	if strings.TrimSpace(flags.approval) != "" {
		args = append(args, "--approval", shellQuote(flags.approval))
	}
	if strings.TrimSpace(flags.color) != "" {
		args = append(args, "--color", shellQuote(flags.color))
	}
	if strings.TrimSpace(initialPrompt) != "" {
		args = append(args, "--prompt", shellQuote(initialPrompt))
	}
	return strings.Join(args, " "), nil
}

func resolveWorkspace(store agent.Store, id string) (agent.Workspace, error) {
	if workspace, err := store.Load(id); err != nil {
		return agent.Workspace{}, err
	} else if workspace.Key != "" {
		return workspace, nil
	}
	workspaces, err := store.List()
	if err != nil {
		return agent.Workspace{}, err
	}
	for _, workspace := range workspaces {
		if workspace.TmuxTarget == id || workspace.TmuxSession == id || filepath.Base(workspace.CWD) == id {
			return workspace, nil
		}
	}
	return agent.Workspace{}, fmt.Errorf("agent workspace %q was not found", id)
}

func shellQuote(value string) string {
	return fmt.Sprintf("%q", value)
}
