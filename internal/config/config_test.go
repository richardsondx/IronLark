package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMergesUserAndProjectConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cwd := t.TempDir()
	userPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lark", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	userConfig := []byte(`
default_provider: openrouter
default_model: openai/gpt-4.1-mini
approval_mode: auto-safe
security:
  redact_patterns:
    - INTERNAL_TOKEN
`)
	if err := os.WriteFile(userPath, userConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	projectConfig := []byte(`
project:
  name: api
  stack: go
  services:
    - api.service
  protected_paths:
    - secrets.yml
`)
	if err := os.WriteFile(filepath.Join(cwd, ".lark.yaml"), projectConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(cwd)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Merged.DefaultProvider != "openrouter" {
		t.Fatalf("expected provider override, got %q", loaded.Merged.DefaultProvider)
	}
	if loaded.Project.Project.Name != "api" {
		t.Fatalf("expected project name api, got %q", loaded.Project.Project.Name)
	}
	if !contains(loaded.Merged.Security.ProtectedPaths, "secrets.yml") {
		t.Fatalf("expected merged protected paths to include project override")
	}
	if !contains(loaded.Merged.Security.RedactPatterns, "INTERNAL_TOKEN") {
		t.Fatalf("expected merged redact patterns to include user override")
	}
}

func TestSetValueSupportsProviderFields(t *testing.T) {
	cfg := Config{}
	if err := SetValue(&cfg, "providers.openai.base_url", "https://example.com/v1"); err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["openai"].BaseURL != "https://example.com/v1" {
		t.Fatalf("unexpected provider value %q", cfg.Providers["openai"].BaseURL)
	}
}

func TestSetValueSupportsInteractionMode(t *testing.T) {
	cfg := Config{}
	if err := SetValue(&cfg, "interaction_mode", "plan-first"); err != nil {
		t.Fatal(err)
	}
	if cfg.InteractionMode != "plan-first" {
		t.Fatalf("unexpected interaction mode %q", cfg.InteractionMode)
	}
}

func TestUpsertEnvValueCreatesAndUpdatesEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := UpsertEnvValue(path, "OPENAI_API_KEY", "one"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertEnvValue(path, "OPENAI_API_KEY", "two"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertEnvValue(path, "OTHER_TOKEN", "three"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "OPENAI_API_KEY=two\n") {
		t.Fatalf("expected updated key, got %q", got)
	}
	if !strings.Contains(got, "OTHER_TOKEN=three\n") {
		t.Fatalf("expected second key, got %q", got)
	}
}

func TestLoadReadsEnvFilesWithoutOverridingShellEnv(t *testing.T) {
	configHome := t.TempDir()
	dataHome := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("OPENAI_API_KEY", "from-shell")

	userEnvPath := filepath.Join(configHome, "lark", ".env")
	if err := os.MkdirAll(filepath.Dir(userEnvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userEnvPath, []byte("OPENROUTER_API_KEY=from-user-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("OPENAI_API_KEY=from-project-env\nPROJECT_ONLY_TOKEN=project\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(cwd); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "from-shell" {
		t.Fatalf("expected shell env to win, got %q", got)
	}
	if got := os.Getenv("OPENROUTER_API_KEY"); got != "from-user-env" {
		t.Fatalf("expected user env file value, got %q", got)
	}
	if got := os.Getenv("PROJECT_ONLY_TOKEN"); got != "project" {
		t.Fatalf("expected project env value, got %q", got)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
