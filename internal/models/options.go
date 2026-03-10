package models

import (
	"fmt"
	"slices"
	"strings"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
)

type ProviderOptions struct {
	Provider string
	Models   []string
}

func SuggestedForProvider(cfg cfgpkg.Config, provider string) []string {
	for _, option := range Suggested(cfg) {
		if option.Provider == strings.TrimSpace(provider) {
			return append([]string(nil), option.Models...)
		}
	}
	return nil
}

func Suggested(cfg cfgpkg.Config) []ProviderOptions {
	byProvider := map[string]map[string]struct{}{}

	add := func(providerName, model string) {
		providerName = strings.TrimSpace(providerName)
		model = strings.TrimSpace(model)
		if providerName == "" || model == "" {
			return
		}
		if byProvider[providerName] == nil {
			byProvider[providerName] = map[string]struct{}{}
		}
		byProvider[providerName][model] = struct{}{}
	}

	for providerName, providerCfg := range cfg.Providers {
		add(providerName, providerCfg.DefaultModel)
	}
	for _, profile := range cfg.Profiles {
		add(profile.Provider, profile.Model)
	}

	// Keep suggested IDs conservative and aligned with models already referenced
	// in the repo config so `/model` shows practical alternatives by default.
	add("openai", "gpt-5")
	add("openai", "gpt-5-mini")
	add("openai", "gpt-4.1-mini")
	add("openrouter", "openai/gpt-4.1-mini")

	providers := make([]string, 0, len(byProvider))
	for providerName := range byProvider {
		providers = append(providers, providerName)
	}
	slices.Sort(providers)

	options := make([]ProviderOptions, 0, len(providers))
	for _, providerName := range providers {
		models := make([]string, 0, len(byProvider[providerName]))
		for model := range byProvider[providerName] {
			models = append(models, model)
		}
		slices.Sort(models)
		options = append(options, ProviderOptions{
			Provider: providerName,
			Models:   models,
		})
	}
	return options
}

func FormatCurrent(cfg cfgpkg.Config, activeProvider, activeModel string) string {
	options := Suggested(cfg)
	if len(options) == 0 {
		return fmt.Sprintf("Current model: %s", strings.TrimSpace(activeModel))
	}

	reordered := make([]ProviderOptions, 0, len(options))
	for _, option := range options {
		if option.Provider == activeProvider {
			reordered = append(reordered, option)
		}
	}
	for _, option := range options {
		if option.Provider != activeProvider {
			reordered = append(reordered, option)
		}
	}

	lines := []string{
		fmt.Sprintf("Current model: %s", strings.TrimSpace(activeModel)),
		"Available model options:",
	}
	for _, option := range reordered {
		line := fmt.Sprintf("  %s: %s", option.Provider, strings.Join(option.Models, ", "))
		if option.Provider == activeProvider {
			line += " (active provider)"
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Use /model <name> or `lk model <name>` to change.")
	return strings.Join(lines, "\n")
}
