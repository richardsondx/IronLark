package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version         int                       `yaml:"version"`
	DefaultProvider string                    `yaml:"default_provider,omitempty"`
	DefaultModel    string                    `yaml:"default_model,omitempty"`
	DefaultProfile  string                    `yaml:"default_profile,omitempty"`
	ApprovalMode    string                    `yaml:"approval_mode,omitempty"`
	InteractionMode string                    `yaml:"interaction_mode,omitempty"`
	Context         ContextConfig             `yaml:"context,omitempty"`
	Thread          ThreadConfig              `yaml:"thread,omitempty"`
	Tools           ToolConfig                `yaml:"tools,omitempty"`
	Rules           RulesConfig               `yaml:"rules,omitempty"`
	Security        SecurityConfig            `yaml:"security,omitempty"`
	Providers       map[string]ProviderConfig `yaml:"providers,omitempty"`
	Profiles        map[string]ProfileConfig  `yaml:"profiles,omitempty"`
}

type ProjectConfig struct {
	Project  ProjectSettings `yaml:"project,omitempty"`
	Security SecurityConfig  `yaml:"security,omitempty"`
}

type ProjectSettings struct {
	Name           string   `yaml:"name,omitempty"`
	Stack          string   `yaml:"stack,omitempty"`
	RunCommand     string   `yaml:"run_command,omitempty"`
	TestCommand    string   `yaml:"test_command,omitempty"`
	ProtectedPaths []string `yaml:"protected_paths,omitempty"`
	Services       []string `yaml:"services,omitempty"`
}

type ContextConfig struct {
	AutoCollect           bool `yaml:"auto_collect,omitempty"`
	MaxFileBytes          int  `yaml:"max_file_bytes,omitempty"`
	MaxCommandOutputBytes int  `yaml:"max_command_output_bytes,omitempty"`
	MaxSTDINBytes         int  `yaml:"max_stdin_bytes,omitempty"`
	MaxActions            int  `yaml:"max_actions,omitempty"`
	MaxListEntries        int  `yaml:"max_list_entries,omitempty"`
}

type ThreadConfig struct {
	Enabled        *bool   `yaml:"enabled,omitempty"`
	Scope          string  `yaml:"scope,omitempty"`
	MaxTokens      int     `yaml:"max_tokens,omitempty"`
	WarnAtRatio    float64 `yaml:"warn_at_ratio,omitempty"`
	RecentTurns    int     `yaml:"recent_turns,omitempty"`
	MaxResultChars int     `yaml:"max_result_chars,omitempty"`
	AutoCompact    *bool   `yaml:"auto_compact,omitempty"`
}

type ToolConfig struct {
	MaxTurns               int     `yaml:"max_turns,omitempty"`
	MaxConsecutiveFailures int     `yaml:"max_consecutive_failures,omitempty"`
	ConfidenceThreshold    float64 `yaml:"confidence_threshold,omitempty"`
	SemanticMaxFiles       int     `yaml:"semantic_max_files,omitempty"`
	SemanticChunkLines     int     `yaml:"semantic_chunk_lines,omitempty"`
	WebSearchResults       int     `yaml:"web_search_results,omitempty"`
}

type RulesConfig struct {
	RemoteURLs []string `yaml:"remote_urls,omitempty"`
}

type SecurityConfig struct {
	ProtectedPaths       []string `yaml:"protected_paths,omitempty"`
	RedactPatterns       []string `yaml:"redact_patterns,omitempty"`
	AutoApproveReadTools bool     `yaml:"auto_approve_read_tools,omitempty"`
}

type ProviderConfig struct {
	Type         string            `yaml:"type,omitempty"`
	BaseURL      string            `yaml:"base_url,omitempty"`
	APIKeyEnv    string            `yaml:"api_key_env,omitempty"`
	DefaultModel string            `yaml:"default_model,omitempty"`
	Headers      map[string]string `yaml:"headers,omitempty"`
}

type ProfileConfig struct {
	Provider string `yaml:"provider,omitempty"`
	Model    string `yaml:"model,omitempty"`
}

type Paths struct {
	ConfigPath        string
	EnvPath           string
	ProjectConfigPath string
	ProjectEnvPath    string
	SessionsDir       string
	ThreadsDir        string
	PolicyPath        string
	PatchesDir        string
	CheckpointsDir    string
}

type Loaded struct {
	Paths      Paths
	User       Config
	Project    ProjectConfig
	Merged     Config
	WorkingDir string
}

func DefaultConfig() Config {
	return Config{
		Version:         1,
		DefaultProvider: "openai",
		DefaultModel:    "gpt-5-mini",
		DefaultProfile:  "strong",
		ApprovalMode:    "confirm",
		InteractionMode: "execute-first",
		Context: ContextConfig{
			AutoCollect:           true,
			MaxFileBytes:          64 * 1024,
			MaxCommandOutputBytes: 128 * 1024,
			MaxSTDINBytes:         128 * 1024,
			MaxActions:            8,
			MaxListEntries:        50,
		},
		Thread: ThreadConfig{
			Enabled:        boolPtr(true),
			Scope:          "auto-shell",
			MaxTokens:      12000,
			WarnAtRatio:    0.8,
			RecentTurns:    8,
			MaxResultChars: 1200,
			AutoCompact:    boolPtr(true),
		},
		Tools: ToolConfig{
			MaxTurns:               5,
			MaxConsecutiveFailures: 2,
			ConfidenceThreshold:    0.85,
			SemanticMaxFiles:       250,
			SemanticChunkLines:     24,
			WebSearchResults:       5,
		},
		Security: SecurityConfig{
			ProtectedPaths: []string{".env", "/root/.ssh", "/home/*/.ssh"},
			RedactPatterns: []string{
				"OPENAI_API_KEY",
				"OPENROUTER_API_KEY",
				"DATABASE_URL",
				"AWS_SECRET_ACCESS_KEY",
				"AWS_SESSION_TOKEN",
			},
			AutoApproveReadTools: true,
		},
		Providers: map[string]ProviderConfig{
			"openai": {
				Type:         "openai-compatible",
				BaseURL:      "https://api.openai.com/v1",
				APIKeyEnv:    "OPENAI_API_KEY",
				DefaultModel: "gpt-5-mini",
			},
			"openrouter": {
				Type:         "openai-compatible",
				BaseURL:      "https://openrouter.ai/api/v1",
				APIKeyEnv:    "OPENROUTER_API_KEY",
				DefaultModel: "openai/gpt-4.1-mini",
			},
		},
		Profiles: map[string]ProfileConfig{
			"cheap": {
				Provider: "openrouter",
				Model:    "openai/gpt-4.1-mini",
			},
			"balanced": {
				Provider: "openrouter",
				Model:    "openai/gpt-4.1-mini",
			},
			"strong": {
				Provider: "openai",
				Model:    "gpt-5-mini",
			},
			"local": {
				Provider: "openai",
				Model:    "gpt-4.1-mini",
			},
		},
	}
}

func ResolvePaths(cwd string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}

	return Paths{
		ConfigPath:        filepath.Join(configHome, "lark", "config.yaml"),
		EnvPath:           filepath.Join(configHome, "lark", ".env"),
		ProjectConfigPath: filepath.Join(cwd, ".lark.yaml"),
		ProjectEnvPath:    filepath.Join(cwd, ".env"),
		SessionsDir:       filepath.Join(dataHome, "lark", "sessions"),
		ThreadsDir:        filepath.Join(dataHome, "lark", "threads"),
		PolicyPath:        filepath.Join(dataHome, "lark", "policy.json"),
		PatchesDir:        filepath.Join(dataHome, "lark", "patches"),
		CheckpointsDir:    filepath.Join(dataHome, "lark", "checkpoints"),
	}, nil
}

func EnsureDirs(paths Paths) error {
	dirs := []string{
		filepath.Dir(paths.ConfigPath),
		paths.SessionsDir,
		paths.ThreadsDir,
		paths.PatchesDir,
		paths.CheckpointsDir,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func Load(cwd string) (Loaded, error) {
	paths, err := ResolvePaths(cwd)
	if err != nil {
		return Loaded{}, err
	}
	if err := loadEnvFiles(paths.EnvPath, paths.ProjectEnvPath); err != nil {
		return Loaded{}, err
	}

	loaded := Loaded{
		Paths:      paths,
		User:       Config{},
		Merged:     DefaultConfig(),
		WorkingDir: cwd,
	}

	if err := readYAML(paths.ConfigPath, &loaded.User); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("read user config: %w", err)
	}
	loaded.Merged = mergeConfig(DefaultConfig(), loaded.User)

	if err := readYAML(paths.ProjectConfigPath, &loaded.Project); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Loaded{}, fmt.Errorf("read project config: %w", err)
	}

	projectPaths := append([]string{}, loaded.Merged.Security.ProtectedPaths...)
	projectPaths = append(projectPaths, loaded.Project.Project.ProtectedPaths...)
	loaded.Merged.Security.ProtectedPaths = dedupe(projectPaths)

	return loaded, nil
}

func SaveUserConfig(path string, cfg Config) error {
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func UpsertEnvValue(path, key, value string) error {
	values, err := readEnvFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read env file: %w", err)
	}
	if values == nil {
		values = map[string]string{}
	}
	values[key] = value
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create env directory: %w", err)
	}
	lines := make([]string, 0, len(values))
	for _, envKey := range sortedKeys(values) {
		lines = append(lines, fmt.Sprintf("%s=%s", envKey, values[envKey]))
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	return nil
}

func mergeConfig(base Config, override Config) Config {
	out := base
	if override.Version != 0 {
		out.Version = override.Version
	}
	if override.DefaultProvider != "" {
		out.DefaultProvider = override.DefaultProvider
	}
	if override.DefaultModel != "" {
		out.DefaultModel = override.DefaultModel
	}
	if override.DefaultProfile != "" {
		out.DefaultProfile = override.DefaultProfile
	}
	if override.ApprovalMode != "" {
		out.ApprovalMode = override.ApprovalMode
	}
	if override.InteractionMode != "" {
		out.InteractionMode = override.InteractionMode
	}
	if override.Context.MaxFileBytes != 0 {
		out.Context.MaxFileBytes = override.Context.MaxFileBytes
	}
	if override.Context.MaxCommandOutputBytes != 0 {
		out.Context.MaxCommandOutputBytes = override.Context.MaxCommandOutputBytes
	}
	if override.Context.MaxSTDINBytes != 0 {
		out.Context.MaxSTDINBytes = override.Context.MaxSTDINBytes
	}
	if override.Context.MaxActions != 0 {
		out.Context.MaxActions = override.Context.MaxActions
	}
	if override.Context.MaxListEntries != 0 {
		out.Context.MaxListEntries = override.Context.MaxListEntries
	}
	out.Context.AutoCollect = override.Context.AutoCollect || out.Context.AutoCollect
	if override.Thread.Scope != "" {
		out.Thread.Scope = override.Thread.Scope
	}
	if override.Thread.MaxTokens != 0 {
		out.Thread.MaxTokens = override.Thread.MaxTokens
	}
	if override.Thread.WarnAtRatio != 0 {
		out.Thread.WarnAtRatio = override.Thread.WarnAtRatio
	}
	if override.Thread.RecentTurns != 0 {
		out.Thread.RecentTurns = override.Thread.RecentTurns
	}
	if override.Thread.MaxResultChars != 0 {
		out.Thread.MaxResultChars = override.Thread.MaxResultChars
	}
	if override.Thread.Enabled != nil {
		out.Thread.Enabled = boolPtr(*override.Thread.Enabled)
	}
	if override.Thread.AutoCompact != nil {
		out.Thread.AutoCompact = boolPtr(*override.Thread.AutoCompact)
	}
	if override.Tools.MaxTurns != 0 {
		out.Tools.MaxTurns = override.Tools.MaxTurns
	}
	if override.Tools.MaxConsecutiveFailures != 0 {
		out.Tools.MaxConsecutiveFailures = override.Tools.MaxConsecutiveFailures
	}
	if override.Tools.ConfidenceThreshold != 0 {
		out.Tools.ConfidenceThreshold = override.Tools.ConfidenceThreshold
	}
	if override.Tools.SemanticMaxFiles != 0 {
		out.Tools.SemanticMaxFiles = override.Tools.SemanticMaxFiles
	}
	if override.Tools.SemanticChunkLines != 0 {
		out.Tools.SemanticChunkLines = override.Tools.SemanticChunkLines
	}
	if override.Tools.WebSearchResults != 0 {
		out.Tools.WebSearchResults = override.Tools.WebSearchResults
	}
	if override.Security.AutoApproveReadTools {
		out.Security.AutoApproveReadTools = true
	}
	out.Rules.RemoteURLs = dedupe(append(out.Rules.RemoteURLs, override.Rules.RemoteURLs...))
	out.Security.ProtectedPaths = dedupe(append(out.Security.ProtectedPaths, override.Security.ProtectedPaths...))
	out.Security.RedactPatterns = dedupe(append(out.Security.RedactPatterns, override.Security.RedactPatterns...))
	if len(override.Providers) > 0 {
		if out.Providers == nil {
			out.Providers = map[string]ProviderConfig{}
		}
		for name, provider := range override.Providers {
			baseProvider := out.Providers[name]
			if provider.Type != "" {
				baseProvider.Type = provider.Type
			}
			if provider.BaseURL != "" {
				baseProvider.BaseURL = provider.BaseURL
			}
			if provider.APIKeyEnv != "" {
				baseProvider.APIKeyEnv = provider.APIKeyEnv
			}
			if provider.DefaultModel != "" {
				baseProvider.DefaultModel = provider.DefaultModel
			}
			if len(provider.Headers) > 0 {
				if baseProvider.Headers == nil {
					baseProvider.Headers = map[string]string{}
				}
				for k, v := range provider.Headers {
					baseProvider.Headers[k] = v
				}
			}
			out.Providers[name] = baseProvider
		}
	}
	if len(override.Profiles) > 0 {
		if out.Profiles == nil {
			out.Profiles = map[string]ProfileConfig{}
		}
		for name, profile := range override.Profiles {
			out.Profiles[name] = profile
		}
	}
	return out
}

func SetValue(cfg *Config, key, value string) error {
	switch key {
	case "default_provider", "provider":
		cfg.DefaultProvider = value
	case "default_model", "model":
		cfg.DefaultModel = value
	case "default_profile", "profile":
		cfg.DefaultProfile = value
	case "approval_mode", "approval":
		cfg.ApprovalMode = value
	case "interaction_mode":
		cfg.InteractionMode = value
	case "thread.enabled":
		cfg.Thread.Enabled = boolPtr(parseBool(value))
	case "thread.scope":
		cfg.Thread.Scope = value
	case "thread.max_tokens":
		cfg.Thread.MaxTokens = parseInt(value)
	case "thread.warn_at_ratio":
		cfg.Thread.WarnAtRatio = parseFloat(value)
	case "thread.recent_turns":
		cfg.Thread.RecentTurns = parseInt(value)
	case "thread.max_result_chars":
		cfg.Thread.MaxResultChars = parseInt(value)
	case "thread.auto_compact":
		cfg.Thread.AutoCompact = boolPtr(parseBool(value))
	default:
		if strings.HasPrefix(key, "providers.") {
			return setProviderValue(cfg, key, value)
		}
		return fmt.Errorf("unsupported config key %q", key)
	}
	return nil
}

func setProviderValue(cfg *Config, key, value string) error {
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		return fmt.Errorf("provider key must look like providers.<name>.<field>")
	}
	name := parts[1]
	field := parts[2]
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	provider := cfg.Providers[name]
	switch field {
	case "type":
		provider.Type = value
	case "base_url":
		provider.BaseURL = value
	case "api_key_env":
		provider.APIKeyEnv = value
	case "default_model":
		provider.DefaultModel = value
	default:
		return fmt.Errorf("unsupported provider field %q", field)
	}
	cfg.Providers[name] = provider
	return nil
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseInt(value string) int {
	var out int
	fmt.Sscanf(strings.TrimSpace(value), "%d", &out)
	return out
}

func parseFloat(value string) float64 {
	var out float64
	fmt.Sscanf(strings.TrimSpace(value), "%f", &out)
	return out
}

func boolPtr(value bool) *bool {
	return &value
}

func (c ThreadConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c ThreadConfig) AutoCompactValue() bool {
	if c.AutoCompact == nil {
		return true
	}
	return *c.AutoCompact
}

func readYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
}

func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || slices.Contains(out, value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func loadEnvFiles(paths ...string) error {
	for _, path := range paths {
		if err := loadEnvFile(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("load env file %s: %w", path, err)
		}
	}
	return nil
}

func loadEnvFile(path string) error {
	values, err := readEnvFile(path)
	if err != nil {
		return err
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		values[key] = value
	}
	return values, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
