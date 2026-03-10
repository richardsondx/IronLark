package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/richardsondx/IronLark/internal/agent"
	"github.com/richardsondx/IronLark/internal/models"
	"github.com/richardsondx/IronLark/internal/render"
	"github.com/richardsondx/IronLark/internal/sessions"
	"github.com/richardsondx/IronLark/internal/threads"
)

func newAgentCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "agent [initial prompt]",
		Short:        "Start or resume the SSH-first agent workspace",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentWorkspace(cmd.Context(), flags, strings.Join(args, " "))
		},
	}
	cmd.AddCommand(newAgentAttachCommand(flags))
	cmd.AddCommand(newAgentListCommand(flags))
	cmd.AddCommand(newAgentStopCommand(flags))
	cmd.AddCommand(newAgentUICommand(flags))
	cmd.AddCommand(newAgentRunnerCommand(flags))
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
			manager := agent.SessionManager{Store: application.Agents}
			return manager.Attach(cmd.Context(), workspace)
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
			manager := agent.SessionManager{Store: application.Agents}
			refreshed := make([]agent.Workspace, 0, len(workspaces))
			for _, workspace := range workspaces {
				workspace, _ = manager.Inspect(cmd.Context(), workspace)
				refreshed = append(refreshed, workspace)
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(refreshed)
			}
			for _, workspace := range refreshed {
				application.Renderer.Message(fmt.Sprintf("%s  %s  %s  %s  %s  %s",
					workspace.Key,
					workspace.LastActiveAt.Format("2006-01-02 15:04:05"),
					workspace.State,
					workspace.Host,
					workspace.CWD,
					workspace.SessionID,
				))
			}
			return nil
		},
	}
}

func newAgentStopCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <workspace>",
		Short: "Stop an agent workspace and end its session",
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
			manager := agent.SessionManager{Store: application.Agents}
			if err := manager.Stop(cmd.Context(), workspace); err != nil {
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
	var welcomeBack bool
	cmd := &cobra.Command{
		Use:          "__agent-ui",
		Short:        "Internal agent UI runner",
		Hidden:       true,
		SilenceUsage: true,
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
			recentPrompts := recentAgentPrompts(application.Sessions)
			application.Renderer = render.NewAgent(os.Stdin, os.Stdout, os.Stderr, flags.color, render.AgentMeta{
				Host:             workspace.Host,
				CWD:              workspace.CWD,
				Provider:         application.Runtime.ProviderName,
				Model:            application.Runtime.Model,
				ModelOptions:     models.SuggestedForProvider(application.Runtime.Config, application.Runtime.ProviderName),
				RecentPrompts:    recentPrompts,
				WelcomeBack:      welcomeBack,
				ApprovalMode:     application.Runtime.ApprovalMode,
				ThreadID:         workspace.ThreadID,
				CompactAtRows:    application.Runtime.Config.Agent.CompactModeRows,
				Interaction:      application.Runtime.Interaction,
				NarratedProgress: application.Runtime.Config.UI.NarratedProgress,
				PolicyStore:      application.PolicyStore,
				OpsSummary:       application.Ops.SummaryLine(),
			})
			defer fmt.Fprint(os.Stdout, "\033[?25h\033[?2004l\033[?1049l")
			if err := application.Agents.Save(workspace); err != nil {
				return err
			}
			return application.Engine().RunChat(cmd.Context(), initialPrompt, nil)
		},
	}
	cmd.Flags().StringVar(&workspaceKey, "workspace", "", "workspace key")
	cmd.Flags().StringVar(&initialPrompt, "prompt", "", "initial prompt")
	cmd.Flags().BoolVar(&welcomeBack, "welcome-back", false, "render returning-user welcome state")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}

func newAgentRunnerCommand(flags *rootFlags) *cobra.Command {
	var workspaceKey string
	var initialPrompt string
	var welcomeBack bool
	cmd := &cobra.Command{
		Use:          "__agent-runner",
		Short:        "Internal detached agent session runner",
		Hidden:       true,
		SilenceUsage: true,
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
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			uiArgs := []string{"agent", "__agent-ui", "--workspace", workspace.Key, "--thread", workspace.ThreadID}
			if strings.TrimSpace(flags.provider) != "" {
				uiArgs = append(uiArgs, "--provider", flags.provider)
			}
			if strings.TrimSpace(flags.model) != "" {
				uiArgs = append(uiArgs, "--model", flags.model)
			}
			if strings.TrimSpace(flags.profile) != "" {
				uiArgs = append(uiArgs, "--profile", flags.profile)
			}
			if strings.TrimSpace(flags.approval) != "" {
				uiArgs = append(uiArgs, "--approval", flags.approval)
			}
			if strings.TrimSpace(flags.color) != "" {
				uiArgs = append(uiArgs, "--color", flags.color)
			}
			if welcomeBack {
				uiArgs = append(uiArgs, "--welcome-back")
			}
			runner := agent.RunnerServer{
				Store:      application.Agents,
				Workspace:  workspace,
				Executable: executable,
				UIArgs:     uiArgs,
			}
			return runner.Run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&workspaceKey, "workspace", "", "workspace key")
	cmd.Flags().StringVar(&initialPrompt, "prompt", "", "initial prompt")
	cmd.Flags().BoolVar(&welcomeBack, "welcome-back", false, "render returning-user welcome state")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}

func runAgentWorkspace(ctx context.Context, flags *rootFlags, initialPrompt string) error {
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	userName, host := agent.CurrentIdentity()
	ref, err := threads.ResolveDefaultThread(application.Runtime)
	if err != nil {
		return err
	}
	workspace := agent.BuildWorkspace(application.Runtime.Config.Agent.SessionPrefix, application.Loaded.Paths.AgentDir, userName, host, application.Runtime.WorkingDir, ref.ThreadID)
	hadExistingWorkspace := false
	if existing, err := application.Agents.Load(workspace.Key); err == nil && existing.Key != "" {
		hadExistingWorkspace = true
		workspace = mergeWorkspaceDefaults(existing, workspace)
	}
	if strings.TrimSpace(workspace.ThreadID) == "" {
		workspace.ThreadID = ref.ThreadID
	}

	executable, runnerArgs, err := agentRunnerCommand(flags, workspace, hadExistingWorkspace)
	if err != nil {
		return err
	}
	manager := agent.SessionManager{Store: application.Agents}
	workspace, err = manager.EnsureWorkspace(ctx, workspace, executable, runnerArgs)
	if err != nil {
		return err
	}
	if err := manager.AttachWithPrompt(ctx, workspace, initialPrompt); err != nil {
		if !isRecoverableAgentAttachError(err) {
			return err
		}
		_ = manager.Stop(ctx, workspace)
		_ = application.Agents.Delete(workspace.Key)
		workspace = agent.BuildWorkspace(application.Runtime.Config.Agent.SessionPrefix, application.Loaded.Paths.AgentDir, userName, host, application.Runtime.WorkingDir, workspace.ThreadID)
		workspace, err = manager.EnsureWorkspace(ctx, workspace, executable, runnerArgs)
		if err != nil {
			return err
		}
		return manager.AttachWithPrompt(ctx, workspace, initialPrompt)
	}
	return nil
}

func agentRunnerCommand(flags *rootFlags, workspace agent.Workspace, welcomeBack bool) (string, []string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	args := []string{"agent", "__agent-runner", "--workspace", workspace.Key, "--thread", workspace.ThreadID}
	if strings.TrimSpace(flags.provider) != "" {
		args = append(args, "--provider", flags.provider)
	}
	if strings.TrimSpace(flags.model) != "" {
		args = append(args, "--model", flags.model)
	}
	if strings.TrimSpace(flags.profile) != "" {
		args = append(args, "--profile", flags.profile)
	}
	if strings.TrimSpace(flags.approval) != "" {
		args = append(args, "--approval", flags.approval)
	}
	if strings.TrimSpace(flags.color) != "" {
		args = append(args, "--color", flags.color)
	}
	if welcomeBack {
		args = append(args, "--welcome-back")
	}
	return executable, args, nil
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
		if workspace.SessionID == id || filepath.Base(workspace.CWD) == id {
			return workspace, nil
		}
	}
	return agent.Workspace{}, fmt.Errorf("agent workspace %q was not found", id)
}

func mergeWorkspaceDefaults(existing, generated agent.Workspace) agent.Workspace {
	if strings.TrimSpace(existing.User) == "" {
		existing.User = generated.User
	}
	if strings.TrimSpace(existing.Host) == "" {
		existing.Host = generated.Host
	}
	if strings.TrimSpace(existing.CWD) == "" {
		existing.CWD = generated.CWD
	}
	if strings.TrimSpace(existing.ThreadID) == "" {
		existing.ThreadID = generated.ThreadID
	}
	if strings.TrimSpace(existing.SessionID) == "" {
		existing.SessionID = generated.SessionID
	}
	if strings.TrimSpace(existing.SocketPath) == "" {
		existing.SocketPath = generated.SocketPath
	}
	if strings.TrimSpace(existing.State) == "" {
		existing.State = generated.State
	}
	return existing
}

func recentAgentPrompts(store sessions.Store) []string {
	records, err := store.List()
	if err != nil || len(records) == 0 {
		return nil
	}
	prompts := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, record := range records {
		prompt := strings.TrimSpace(lastPromptLine(record.Prompt))
		if prompt == "" {
			continue
		}
		if _, ok := seen[prompt]; ok {
			continue
		}
		seen[prompt] = struct{}{}
		prompts = append(prompts, prompt)
		if len(prompts) == 4 {
			break
		}
	}
	return prompts
}

func lastPromptLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	for idx := len(lines) - 1; idx >= 0; idx-- {
		if trimmed := strings.TrimSpace(lines[idx]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isRecoverableAgentAttachError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "use of closed network connection")
}
