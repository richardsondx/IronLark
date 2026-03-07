package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
)

func newConfigCommand(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage Lark configuration",
	}
	cmd.AddCommand(newConfigInitCommand(flags))
	cmd.AddCommand(newConfigShowCommand(flags))
	cmd.AddCommand(newConfigSetCommand(flags))
	cmd.AddCommand(newConfigUseCommand(flags))
	cmd.AddCommand(newConfigTestCommand(flags))
	return cmd
}

func newConfigInitCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create or overwrite ~/.config/lark/config.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			cfg := cfgpkg.DefaultConfig()
			providerName, err := application.Renderer.ReadPrompt("Select provider [openai]: ")
			if err != nil {
				return err
			}
			if providerName == "" {
				providerName = "openai"
			}
			providerCfg, ok := cfg.Providers[providerName]
			if !ok {
				providerCfg = cfgpkg.ProviderConfig{
					Type:         "openai-compatible",
					BaseURL:      "https://api.openai.com/v1",
					APIKeyEnv:    strings.ToUpper(providerName) + "_API_KEY",
					DefaultModel: cfg.DefaultModel,
				}
			}
			modelName, err := application.Renderer.ReadPrompt(fmt.Sprintf("Default model [%s]: ", cfg.DefaultModel))
			if err != nil {
				return err
			}
			if modelName != "" {
				cfg.DefaultModel = modelName
			}
			apiEnv, err := application.Renderer.ReadPrompt(fmt.Sprintf("API key env var [%s]: ", providerCfg.APIKeyEnv))
			if err != nil {
				return err
			}
			baseURL, err := application.Renderer.ReadPrompt(fmt.Sprintf("Base URL [%s]: ", providerCfg.BaseURL))
			if err != nil {
				return err
			}
			profileName, err := application.Renderer.ReadPrompt(fmt.Sprintf("Default profile [%s]: ", cfg.DefaultProfile))
			if err != nil {
				return err
			}
			approvalMode, err := application.Renderer.ReadPrompt(fmt.Sprintf("Approval mode [%s]: ", cfg.ApprovalMode))
			if err != nil {
				return err
			}

			cfg.DefaultProvider = providerName
			if profileName != "" {
				cfg.DefaultProfile = profileName
			}
			if approvalMode != "" {
				cfg.ApprovalMode = approvalMode
			}

			if apiEnv != "" {
				providerCfg.APIKeyEnv = apiEnv
			}
			if baseURL != "" {
				providerCfg.BaseURL = baseURL
			}
			if modelName != "" {
				providerCfg.DefaultModel = modelName
			}
			cfg.Providers[providerName] = providerCfg

			if err := cfgpkg.SaveUserConfig(application.Loaded.Paths.ConfigPath, cfg); err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Config saved to %s", application.Loaded.Paths.ConfigPath))
			return nil
		},
	}
}

func newConfigShowCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print merged config",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			if application.Runtime.JSONOutput {
				return application.Renderer.MessageJSON(application.Loaded.Merged)
			}
			data, err := marshalYAML(application.Loaded.Merged)
			if err != nil {
				return err
			}
			application.Renderer.Message(data)
			return nil
		},
	}
}

func newConfigSetCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config key in ~/.config/lark/config.yaml",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			loaded, err := cfgpkg.Load(cwd)
			if err != nil {
				return err
			}
			cfg := loaded.User
			if err := cfgpkg.SetValue(&cfg, args[0], args[1]); err != nil {
				return err
			}
			return cfgpkg.SaveUserConfig(loaded.Paths.ConfigPath, cfg)
		},
	}
}

func newConfigUseCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "use <profile>",
		Short: "Set the default profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			loaded, err := cfgpkg.Load(cwd)
			if err != nil {
				return err
			}
			cfg := loaded.User
			cfg.DefaultProfile = args[0]
			return cfgpkg.SaveUserConfig(loaded.Paths.ConfigPath, cfg)
		},
	}
}

func newConfigTestCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Run a small provider request to validate connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			application, err := buildApp(flags)
			if err != nil {
				return err
			}
			if err := providerSmokeTest(cmd.Context(), application); err != nil {
				return err
			}
			application.Renderer.Message("Provider test succeeded.")
			return nil
		},
	}
}
