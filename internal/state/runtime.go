package state

import (
	"fmt"
	"os"
	"strings"

	"github.com/richardsondx/IronLark/internal/config"
	"github.com/richardsondx/IronLark/internal/core"
)

type Runtime struct {
	Config       config.Config
	Project      config.ProjectConfig
	Paths        config.Paths
	WorkingDir   string
	ProviderName string
	Model        string
	ApprovalMode core.ApprovalMode
	ReadOnly     bool
	JSONOutput   bool
	Profile      string
}

type Overrides struct {
	Provider string
	Model    string
	Profile  string
	Approval string
	ReadOnly bool
	JSON     bool
}

func Resolve(loaded config.Loaded, overrides Overrides) (Runtime, error) {
	cfg := loaded.Merged
	profileName := firstNonEmpty(overrides.Profile, cfg.DefaultProfile)
	providerName := firstNonEmpty(overrides.Provider, cfg.DefaultProvider)
	modelName := firstNonEmpty(overrides.Model, cfg.DefaultModel)

	if profileName != "" {
		if profile, ok := cfg.Profiles[profileName]; ok {
			providerName = firstNonEmpty(overrides.Provider, profile.Provider, providerName)
			modelName = firstNonEmpty(overrides.Model, profile.Model, modelName)
		}
	}
	if providerName == "" {
		return Runtime{}, fmt.Errorf("no provider configured")
	}
	if modelName == "" {
		if provider, ok := cfg.Providers[providerName]; ok {
			modelName = provider.DefaultModel
		}
	}
	if err := validateModelForProvider(providerName, modelName); err != nil {
		return Runtime{}, err
	}
	approval := core.ApprovalMode(firstNonEmpty(overrides.Approval, cfg.ApprovalMode))
	if approval == "" {
		approval = core.ApprovalConfirm
	}
	if !approval.Valid() {
		return Runtime{}, fmt.Errorf("invalid approval mode %q", approval)
	}

	return Runtime{
		Config:       cfg,
		Project:      loaded.Project,
		Paths:        loaded.Paths,
		WorkingDir:   loaded.WorkingDir,
		ProviderName: providerName,
		Model:        modelName,
		ApprovalMode: approval,
		ReadOnly:     overrides.ReadOnly,
		JSONOutput:   overrides.JSON,
		Profile:      profileName,
	}, nil
}

func (r Runtime) ProviderConfig() (config.ProviderConfig, error) {
	provider, ok := r.Config.Providers[r.ProviderName]
	if !ok {
		return config.ProviderConfig{}, fmt.Errorf("provider %q not configured", r.ProviderName)
	}
	return provider, nil
}

func (r Runtime) APIKey() (string, error) {
	provider, err := r.ProviderConfig()
	if err != nil {
		return "", err
	}
	if provider.APIKeyEnv == "" {
		return "", fmt.Errorf("provider %q does not have api_key_env configured", r.ProviderName)
	}

	// If it looks like a literal key, return it directly
	if strings.HasPrefix(provider.APIKeyEnv, "sk-") {
		return provider.APIKeyEnv, nil
	}

	value := strings.TrimSpace(os.Getenv(provider.APIKeyEnv))
	if value == "" {
		return "", fmt.Errorf("environment variable %s is not set", provider.APIKeyEnv)
	}
	return value, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validateModelForProvider(providerName, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model cannot be empty")
	}
	switch providerName {
	case "openai":
		if strings.Contains(model, "codex") {
			return fmt.Errorf("invalid configured model %q for provider %q: IronLark currently uses the OpenAI chat-completions API, so use gpt-5-mini instead", model, providerName)
		}
		if strings.Contains(model, "/") {
			return fmt.Errorf("invalid configured model %q for provider %q: use a raw OpenAI model ID like gpt-5-mini or gpt-4.1-mini", model, providerName)
		}
	}
	return nil
}
