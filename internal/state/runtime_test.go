package state

import (
	"strings"
	"testing"

	"github.com/richardsondx/IronLark/internal/config"
)

func TestResolveAllowsResponsesCompatibleOpenAIModel(t *testing.T) {
	loaded := config.Loaded{
		Merged: config.DefaultConfig(),
	}
	loaded.Merged.DefaultProvider = "openai"
	loaded.Merged.DefaultProfile = ""
	loaded.Merged.DefaultModel = "gpt-5"

	if _, err := Resolve(loaded, Overrides{}); err != nil {
		t.Fatalf("expected model to resolve, got %v", err)
	}
}

func TestResolveRejectsOpenAIProviderModelPathStyleID(t *testing.T) {
	loaded := config.Loaded{
		Merged: config.DefaultConfig(),
	}
	loaded.Merged.DefaultProvider = "openai"
	loaded.Merged.DefaultProfile = ""
	loaded.Merged.DefaultModel = "openai/gpt-5-mini"

	_, err := Resolve(loaded, Overrides{})
	if err == nil {
		t.Fatal("expected invalid model error")
	}
	if !strings.Contains(err.Error(), "raw OpenAI model ID") {
		t.Fatalf("expected raw model guidance, got %v", err)
	}
}

func TestDefaultConfigEnablesNarratedProgress(t *testing.T) {
	cfg := config.DefaultConfig()
	if !cfg.UI.NarratedProgress {
		t.Fatal("expected narrated progress to be enabled by default")
	}
}
