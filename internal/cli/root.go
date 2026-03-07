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
	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/state"
)

type rootFlags struct {
	provider string
	model    string
	profile  string
	approval string
	readOnly bool
	json     bool
}

func NewRootCommand() *cobra.Command {
	flags := &rootFlags{}
	cmd := &cobra.Command{
		Use:   "lark [task]",
		Short: "Lark is an SSH-first AI CLI for server and repo operations",
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
	cmd.PersistentFlags().BoolVar(&flags.readOnly, "read-only", false, "block mutating actions")
	cmd.PersistentFlags().BoolVar(&flags.json, "json", false, "print JSON output")

	cmd.AddCommand(newChatCommand(flags))
	cmd.AddCommand(newInspectCommand(flags))
	cmd.AddCommand(newEditCommand(flags))
	cmd.AddCommand(newRunCommand(flags))
	cmd.AddCommand(newHistoryCommand(flags))
	cmd.AddCommand(newUndoCommand(flags))
	cmd.AddCommand(newRestoreCommand(flags))
	cmd.AddCommand(newInitCommand(flags))
	cmd.AddCommand(newModelsCommand(flags))
	cmd.AddCommand(newModelCommand(flags))
	cmd.AddCommand(newConfigCommand(flags))
	cmd.AddCommand(newDoctorCommand(flags))
	return cmd
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
	cfg := loaded.User
	cfg.DefaultModel = model
	cfg.DefaultProfile = ""
	if err := cfgpkg.SaveUserConfig(loaded.Paths.ConfigPath, cfg); err != nil {
		return err
	}
	fmt.Printf("Default model set to %s\n", model)
	return nil
}

func buildApp(flags *rootFlags) (*app.App, error) {
	return app.New(state.Overrides{
		Provider: flags.provider,
		Model:    flags.model,
		Profile:  flags.profile,
		Approval: flags.approval,
		ReadOnly: flags.readOnly,
		JSON:     flags.json,
	})
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
