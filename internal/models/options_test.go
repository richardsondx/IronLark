package models

import (
	"strings"
	"testing"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
)

func TestFormatCurrentIncludesActiveAndSuggestedOptions(t *testing.T) {
	text := FormatCurrent(cfgpkg.DefaultConfig(), "openai", "gpt-4.1-mini")

	for _, fragment := range []string{
		"Current model: gpt-4.1-mini",
		"Available model options:",
		"openai: gpt-4.1-mini, gpt-5-mini (active provider)",
		"openrouter: openai/gpt-4.1-mini",
		"Use /model <name> or `lk model <name>` to change.",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("expected %q in output, got %q", fragment, text)
		}
	}
}
