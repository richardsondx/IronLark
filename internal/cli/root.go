package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/richardsondx/IronLark/internal/app"
	"github.com/richardsondx/IronLark/internal/buildinfo"
	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/state"
	"github.com/richardsondx/IronLark/internal/threads"
	"github.com/richardsondx/IronLark/internal/update"
)

type rootFlags struct {
	provider  string
	model     string
	profile   string
	approval  string
	color     string
	readOnly  bool
	json      bool
	threadID  string
	noContext bool
	newThread bool
}

func NewRootCommand() *cobra.Command {
	flags := &rootFlags{}
	cmd := &cobra.Command{
		Use:     "lark [task]",
		Short:   "Lark is an SSH-first AI CLI for server and repo operations",
		Args:    cobra.ArbitraryArgs,
		Version: buildinfo.Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}

			stdin, err := readPipedInput(application.Runtime.Config.Context.MaxSTDINBytes)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if stdin == nil && term.IsTerminal(int(os.Stdin.Fd())) {
					return application.Engine().RunChat(cmd.Context(), "", nil)
				}
				prompt := "Summarize this input, explain any failures, and propose the next safe step."
				return application.Engine().RunTask(cmd.Context(), prompt, stdin, "oneshot")
			}
			return application.Engine().RunTask(cmd.Context(), strings.Join(args, " "), stdin, "oneshot")
		},
	}

	cmd.PersistentFlags().StringVarP(&flags.provider, "provider", "p", "", "override provider")
	cmd.PersistentFlags().StringVarP(&flags.model, "model", "m", "", "override model")
	cmd.PersistentFlags().StringVar(&flags.profile, "profile", "", "use configured profile")
	cmd.PersistentFlags().StringVar(&flags.approval, "approval", "", "approval mode: suggest|confirm|auto-safe|agent")
	cmd.PersistentFlags().StringVar(&flags.color, "color", "auto", "color mode: auto|always|never")
	cmd.PersistentFlags().BoolVar(&flags.readOnly, "read-only", false, "block mutating actions")
	cmd.PersistentFlags().BoolVar(&flags.json, "json", false, "print JSON output")
	cmd.PersistentFlags().StringVar(&flags.threadID, "thread", "", "use a specific context thread")
	cmd.PersistentFlags().BoolVar(&flags.noContext, "no-context", false, "disable thread context for this run")
	cmd.PersistentFlags().BoolVar(&flags.newThread, "new-thread", false, "start a fresh context thread for this run")

	cmd.AddCommand(newChatCommand(flags))
	cmd.AddCommand(newContextCommand(flags))
	cmd.AddCommand(newInspectCommand(flags))
	cmd.AddCommand(newEditCommand(flags))
	cmd.AddCommand(newRunCommand(flags))
	cmd.AddCommand(newHistoryCommand(flags))
	cmd.AddCommand(newUndoCommand(flags))
	cmd.AddCommand(newRestoreCommand(flags))
	cmd.AddCommand(newInitCommand(flags))
	cmd.AddCommand(newUpdateCommand())
	cmd.AddCommand(newVersionCommand())
	cmd.AddCommand(newModelsCommand(flags))
	cmd.AddCommand(newModelCommand(flags))
	cmd.AddCommand(newConfigCommand(flags))
	cmd.AddCommand(newDoctorCommand(flags))
	return cmd
}

func newVersionCommand() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the installed version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if verbose {
				fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Summary())
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Version)
			return nil
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "show build metadata")
	return cmd
}

func newUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update IronLark to the latest GitHub release",
		RunE: func(cmd *cobra.Command, args []string) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			releaseTag, err := update.Client{RepoSlug: buildinfo.RepoSlug}.UpdateExecutable(cmd.Context(), executable)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated to %s\n", releaseTag)
			return nil
		},
	}
}

func newChatCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "chat [initial prompt]",
		Short: "Start an interactive Lark session",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			stdin, err := readPipedInput(application.Runtime.Config.Context.MaxSTDINBytes)
			if err != nil {
				return err
			}
			return application.Engine().RunChat(cmd.Context(), strings.Join(args, " "), stdin)
		},
	}
}

func newContextCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Inspect and manage thread context",
		RunE: func(cmd *cobra.Command, args []string) error {
			return showContextStatus(flags, false)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "window",
		Short: "Show what context will be injected into the next request",
		RunE: func(cmd *cobra.Command, args []string) error {
			return showContextStatus(flags, true)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear the active thread history",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, ref, err := buildAppWithThread(flags)
			if err != nil {
				return err
			}
			if err := application.Threads.Clear(ref.ThreadID); err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Cleared thread %s", ref.ThreadID))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "drop",
		Short: "Delete the active thread",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, ref, err := buildAppWithThread(flags)
			if err != nil {
				return err
			}
			if err := application.Threads.Delete(ref.ThreadID); err != nil {
				return err
			}
			if err := application.Threads.ClearOverride(ref); err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Deleted thread %s", ref.ThreadID))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "use <thread-id>",
		Short: "Set the manual thread override for this working directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, ref, err := buildAppWithThread(flags)
			if err != nil {
				return err
			}
			ref.ThreadID = args[0]
			ref.Scope = "manual"
			ref.Source = "override"
			ref.Manual = true
			if err := application.Threads.UpsertOverride(ref); err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Active override set to thread %s", ref.ThreadID))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recent threads",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			records, err := application.Threads.List()
			if err != nil {
				return err
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(records)
			}
			for _, record := range records {
				application.Renderer.Message(fmt.Sprintf("%s  %s  turns=%d  tokens=%d  cwd=%s",
					record.ID, record.UpdatedAt.Format("2006-01-02 15:04:05"), len(record.Turns), record.EstimatedTokens, record.CWD))
			}
			return nil
		},
	})
	return cmd
}

func newInspectCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect [system|repo]",
		Short: "Inspect the current machine or repo",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			snapshot, err := application.Collector.Collect(cmd.Context(), application.Runtime, nil)
			if err != nil {
				return err
			}

			switch firstArg(args) {
			case "":
			case "system":
				snapshot.Repo = ctxpkg.RepoSnapshot{}
			case "repo":
				snapshot.System = map[string]any{}
			default:
				return fmt.Errorf("unknown inspect target %q", args[0])
			}
			return application.Renderer.Snapshot(snapshot)
		},
	}
}

func newDoctorCommand(flags *rootFlags) *cobra.Command {
	cmd := newInspectCommand(flags)
	cmd.Use = "doctor [system|repo]"
	cmd.Short = "Alias for inspect"
	return cmd
}

func newEditCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <path> [instruction]",
		Short: "Run an AI patch flow for a specific file",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			max := application.Runtime.Config.Context.MaxFileBytes
			if max > 0 && len(content) > max {
				content = content[:max]
			}
			instruction := strings.TrimSpace(strings.Join(args[1:], " "))
			if instruction == "" {
				instruction, err = application.Renderer.ReadPrompt("Instruction: ")
				if err != nil {
					return err
				}
			}
			prompt := fmt.Sprintf("Patch this file.\nPath: %s\nInstruction: %s\nCurrent file:\n%s", path, instruction, string(content))
			return application.Engine().RunTask(cmd.Context(), prompt, nil, "edit")
		},
	}
}

func newRunCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "run <command>",
		Short: "Run a shell command with policy guardrails",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			action := core.Action{
				ID:         "run-1",
				Type:       core.ActionRun,
				Title:      args[0],
				Command:    args[0],
				Reason:     "user requested direct shell execution",
				TimeoutSec: 60,
			}
			report, err := application.Executor.Preview(action, application.Runtime.ReadOnly)
			if err != nil {
				return err
			}
			application.Renderer.PlannedActions([]core.Action{action}, []core.RiskReport{report})
			if application.Runtime.ApprovalMode == core.ApprovalSuggest {
				return nil
			}
			if application.Executor.Classifier.NeedsApproval(action, report, application.Runtime.ApprovalMode, application.Runtime.Config.Security.AutoApproveReadTools, application.Runtime.ReadOnly) {
				ok, err := application.Renderer.Confirm(action.Title, application.Executor.Classifier.RequiresDoubleConfirm(report))
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			result, err := application.Executor.Execute(cmd.Context(), action, application.Runtime.ReadOnly)
			application.Renderer.Result(result)
			return err
		},
	}
}

func newHistoryCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "history [sessions|patches|checkpoints]",
		Short: "Show local session, patch, or checkpoint history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			switch firstArg(args) {
			case "", "sessions":
				records, err := application.Sessions.List()
				if err != nil {
					return err
				}
				return application.Renderer.Sessions(records)
			case "patches":
				records, err := application.Patches.List()
				if err != nil {
					return err
				}
				return application.Renderer.Patches(records)
			case "checkpoints":
				records, err := application.Checkpoints.List()
				if err != nil {
					return err
				}
				return application.Renderer.Checkpoints(records)
			default:
				return fmt.Errorf("unknown history target %q", args[0])
			}
		},
	}
}

func newUndoCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "undo <patch-id>",
		Short: "Restore a saved file backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			ok, err := application.Renderer.Confirm("restore patch "+args[0], false)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			record, err := application.Patches.Undo(args[0])
			if err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Restored %s from %s", record.Path, record.BackupPath))
			return nil
		},
	}
}

func newRestoreCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <checkpoint-id>",
		Short: "Restore a saved checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			ok, err := application.Renderer.Confirm("restore checkpoint "+args[0], false)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			record, err := application.Checkpoints.Restore(args[0])
			if err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Restored checkpoint %s (%d file(s))", record.ID, len(record.Files)))
			return nil
		},
	}
}

func newModelsCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Show configured providers, profiles, and defaults",
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderModelsOverview(flags)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured models",
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderModelList(flags)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "current",
		Short: "Show the current model",
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderCurrentModel(flags)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <model>",
		Short: "Set the default model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setDefaultModel(args[0])
		},
	})
	return cmd
}

func newModelCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "model [name]",
		Short: "Show or set the default model",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return renderCurrentModel(flags)
			}
			return setDefaultModel(args[0])
		},
	}
}

func renderModelsOverview(flags *rootFlags) error {
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	if application.Runtime.JSONOutput {
		return application.Renderer.MessageJSON(application.Loaded.Merged)
	}
	application.Renderer.Message("Providers:")
	for name, providerCfg := range application.Loaded.Merged.Providers {
		application.Renderer.Message(fmt.Sprintf("- %s (%s) default=%s", name, providerCfg.BaseURL, providerCfg.DefaultModel))
	}
	application.Renderer.Message("\nProfiles:")
	for name, profile := range application.Loaded.Merged.Profiles {
		application.Renderer.Message(fmt.Sprintf("- %s -> %s / %s", name, profile.Provider, profile.Model))
	}
	application.Renderer.Message(fmt.Sprintf("\nActive: provider=%s model=%s profile=%s", application.Runtime.ProviderName, application.Runtime.Model, application.Runtime.Profile))
	return nil
}

func renderModelList(flags *rootFlags) error {
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	if application.Runtime.JSONOutput {
		models := make(map[string]string, len(application.Loaded.Merged.Providers))
		for name, providerCfg := range application.Loaded.Merged.Providers {
			models[name] = providerCfg.DefaultModel
		}
		return application.Renderer.MessageJSON(models)
	}
	for name, providerCfg := range application.Loaded.Merged.Providers {
		application.Renderer.Message(fmt.Sprintf("%s\t%s", name, providerCfg.DefaultModel))
	}
	return nil
}

func renderCurrentModel(flags *rootFlags) error {
	application, err := buildApp(flags)
	if err != nil {
		return err
	}
	if application.Runtime.JSONOutput {
		return application.Renderer.MessageJSON(map[string]string{
			"provider": application.Runtime.ProviderName,
			"model":    application.Runtime.Model,
			"profile":  application.Runtime.Profile,
		})
	}
	application.Renderer.Message(application.Runtime.Model)
	return nil
}

func setDefaultModel(model string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	loaded, err := cfgpkg.Load(cwd)
	if err != nil {
		return err
	}
	providerName := loaded.Merged.DefaultProvider
	if loaded.Merged.DefaultProfile != "" {
		if profile, ok := loaded.Merged.Profiles[loaded.Merged.DefaultProfile]; ok && strings.TrimSpace(profile.Provider) != "" {
			providerName = profile.Provider
		}
	}
	if err := validateModelForProvider(providerName, model); err != nil {
		return err
	}
	cfg := loaded.User
	cfg.DefaultModel = model
	cfg.DefaultProfile = ""
	if err := cfgpkg.SaveUserConfig(loaded.Paths.ConfigPath, cfg); err != nil {
		return err
	}
	fmt.Printf("Default model set to %s\n", model)
	return nil
}

func validateModelForProvider(providerName, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model cannot be empty")
	}
	switch providerName {
	case "openai":
		if strings.Contains(model, "codex") {
			return fmt.Errorf("model %q is not supported by IronLark's current OpenAI chat-completions client; use gpt-5-mini instead", model)
		}
		if strings.Contains(model, "/") {
			return fmt.Errorf("model %q is not a valid raw OpenAI model ID; use values like gpt-5-mini or gpt-4.1-mini", model)
		}
	}
	return nil
}

func buildApp(flags *rootFlags) (*app.App, error) {
	return app.New(state.Overrides{
		Provider:  flags.provider,
		Model:     flags.model,
		Profile:   flags.profile,
		Approval:  flags.approval,
		Color:     flags.color,
		ReadOnly:  flags.readOnly,
		JSON:      flags.json,
		ThreadID:  flags.threadID,
		NoContext: flags.noContext,
		NewThread: flags.newThread,
	})
}

func buildAppWithThread(flags *rootFlags) (*app.App, threads.ThreadRef, error) {
	application, err := buildApp(flags)
	if err != nil {
		return nil, threads.ThreadRef{}, err
	}
	ref, err := threads.ResolveDefaultThread(application.Runtime)
	if err != nil {
		return nil, threads.ThreadRef{}, err
	}
	return application, ref, nil
}

func showContextStatus(flags *rootFlags, includeWindow bool) error {
	application, ref, err := buildAppWithThread(flags)
	if err != nil {
		return err
	}
	stats, err := application.Threads.Stats(ref.ThreadID, application.Runtime.Config.Thread, ref.Source)
	if err != nil {
		return err
	}
	if application.Runtime.JSONOutput {
		return application.Renderer.MessageJSON(stats)
	}
	application.Renderer.Message(fmt.Sprintf("Thread: %s", stats.ThreadID))
	application.Renderer.Message(fmt.Sprintf("Scope: %s (%s)", stats.Scope, stats.Source))
	application.Renderer.Message(fmt.Sprintf("CWD: %s", stats.CWD))
	application.Renderer.Message(fmt.Sprintf("Updated: %s", stats.LastUpdated.Format("2006-01-02 15:04:05")))
	application.Renderer.Message(fmt.Sprintf("Turns: %d", stats.TurnCount))
	application.Renderer.Message(fmt.Sprintf("Estimated tokens: %d/%d", stats.EstimatedTokens, stats.MaxTokens))
	application.Renderer.Message(fmt.Sprintf("Warning: %t", stats.Warning))
	if includeWindow {
		application.Renderer.Message(fmt.Sprintf("Injected messages: %d", stats.PreviewMessageCount))
		if stats.RollingSummary != "" {
			application.Renderer.Message("Summary:")
			application.Renderer.Message(stats.RollingSummary)
		}
		if len(stats.RecentTurns) > 0 {
			application.Renderer.Message("Recent turns:")
			for _, turn := range stats.RecentTurns {
				application.Renderer.Message("- " + turn)
			}
		}
	}
	return nil
}

func readPipedInput(limit int) ([]byte, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, nil
	}
	data, err := os.ReadFile("/dev/stdin")
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(data) > limit {
		data = data[:limit]
	}
	return data, nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func providerSmokeTest(ctx context.Context, application *app.App) error {
	if application.Provider == nil {
		return errors.New("provider is not configured or API key is unavailable")
	}
	_, err := application.Provider.Generate(ctx, provider.Request{
		Model:  application.Runtime.Model,
		System: provider.BuildSystemPrompt(1),
		Messages: []core.ConversationMessage{
			{Role: "user", Content: `Return {"summary":"ok","findings":[],"actions":[],"verification":[],"needs_user_input":false}`},
		},
		Temperature: 0,
	})
	return err
}

func marshalYAML(v any) (string, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
