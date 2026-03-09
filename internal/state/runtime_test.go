package state

import (
	"strings"
	"testing"

	"github.com/richardsondx/IronLark/internal/config"
)

func TestResolveRejectsUnsupportedOpenAIModel(t *testing.T) {
	loaded := config.Loaded{
		Merged: config.DefaultConfig(),
	}
	loaded.Merged.DefaultProvider = "openai"
	loaded.Merged.DefaultProfile = ""
	loaded.Merged.DefaultModel = "gpt-5.3-codex"

	_, err := Resolve(loaded, Overrides{})
	if err == nil {
		t.Fatal("expected invalid model error")
	}
	if !strings.Contains(err.Error(), "gpt-5-mini") {
		t.Fatalf("expected guidance toward gpt-5-mini, got %v", err)
	}
}

func TestDefaultConfigEnablesNarratedProgress(t *testing.T) {
	cfg := config.DefaultConfig()
	if !cfg.UI.NarratedProgress {
		t.Fatal("expected narrated progress to be enabled by default")
	}
}
