package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
		Short: "Interactive setup for config, env, and shell PATH",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInteractiveInit(flags)
		},
	}
}

func newInitCommand(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Set up OpenAI auth, defaults, and shell PATH",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInteractiveInit(flags)
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

func runInteractiveInit(flags *rootFlags) error {
	application, err := buildApp(flags)
	if err != nil {
		return err
	}

	cfg := cfgpkg.DefaultConfig()
	modelName, err := application.Renderer.ReadPrompt(fmt.Sprintf("Model [%s]: ", cfg.DefaultModel))
	if err != nil {
		return err
	}
	if strings.TrimSpace(modelName) != "" {
		cfg.DefaultModel = strings.TrimSpace(modelName)
	}
	cfg.DefaultProvider = "openai"
	cfg.DefaultProfile = "strong"
	if providerCfg, ok := cfg.Providers["openai"]; ok {
		providerCfg.DefaultModel = cfg.DefaultModel
		cfg.Providers["openai"] = providerCfg
	}
	if profileCfg, ok := cfg.Profiles["strong"]; ok {
		profileCfg.Model = cfg.DefaultModel
		cfg.Profiles["strong"] = profileCfg
	}

	apiKey, err := readSecret("OpenAI API key (stored in ~/.config/lark/.env): ")
	if err != nil {
		return err
	}
	if strings.TrimSpace(apiKey) == "" {
		if existing := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); existing != "" {
			apiKey = existing
		} else {
			return fmt.Errorf("OPENAI_API_KEY is required for setup")
		}
	}

	if err := cfgpkg.SaveUserConfig(application.Loaded.Paths.ConfigPath, cfg); err != nil {
		return err
	}
	if err := cfgpkg.UpsertEnvValue(application.Loaded.Paths.EnvPath, "OPENAI_API_KEY", apiKey); err != nil {
		return err
	}

	profilePath, exportLine := detectShellProfile()
	installBin := filepath.Join(mustUserHomeDir(), ".local", "bin")
	if profilePath != "" && !pathContains(os.Getenv("PATH"), installBin) {
		addPath, err := application.Renderer.ReadPrompt(fmt.Sprintf("Add %q to %s? [Y/n] ", exportLine, profilePath))
		if err != nil {
			return err
		}
		if addPath == "" || strings.EqualFold(addPath, "y") || strings.EqualFold(addPath, "yes") {
			if err := ensureLineInFile(profilePath, exportLine); err != nil {
				return err
			}
			application.Renderer.Message(fmt.Sprintf("Added PATH export to %s", profilePath))
			application.Renderer.Message(fmt.Sprintf("Run: source %s", profilePath))
		}
	}

	application.Renderer.Message(fmt.Sprintf("Config saved to %s", application.Loaded.Paths.ConfigPath))
	application.Renderer.Message(fmt.Sprintf("OpenAI key saved to %s", application.Loaded.Paths.EnvPath))
	application.Renderer.Message(`Next: lk "why is nginx failing?"`)
	return nil
}

func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stdout, prompt)
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stdout)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret)), nil
}

func detectShellProfile() (string, string) {
	home := mustUserHomeDir()
	shell := filepath.Base(strings.TrimSpace(os.Getenv("SHELL")))
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc"), fmt.Sprintf(`export PATH="%s:$PATH"`, filepath.Join(home, ".local", "bin"))
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish"), fmt.Sprintf(`fish_add_path %s`, filepath.Join(home, ".local", "bin"))
	default:
		return filepath.Join(home, ".bashrc"), fmt.Sprintf(`export PATH="%s:$PATH"`, filepath.Join(home, ".local", "bin"))
	}
}

func mustUserHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func ensureLineInFile(path, line string) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(content), line) {
		return nil
	}
	block := "\n# >>> ironlark-managed path >>>\n" + line + "\n# <<< ironlark-managed path <<<\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(content, []byte(block)...), 0o644)
}

func pathContains(pathEnv, dir string) bool {
	for _, part := range filepath.SplitList(pathEnv) {
		if part == dir {
			return true
		}
	}
	return false
}
