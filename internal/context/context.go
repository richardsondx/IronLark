package context

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	cfgpkg "github.com/richardson/lark/internal/config"
	"github.com/richardson/lark/internal/redact"
	"github.com/richardson/lark/internal/state"
)

type Snapshot struct {
	System  map[string]any  `json:"system"`
	Repo    RepoSnapshot    `json:"repo"`
	STDIN   string          `json:"stdin,omitempty"`
	Project ProjectSnapshot `json:"project,omitempty"`
}

type ProjectSnapshot struct {
	Name        string   `json:"name,omitempty"`
	Stack       string   `json:"stack,omitempty"`
	RunCommand  string   `json:"run_command,omitempty"`
	TestCommand string   `json:"test_command,omitempty"`
	Services    []string `json:"services,omitempty"`
}

type RepoSnapshot struct {
	Detected      bool              `json:"detected"`
	Root          string            `json:"root,omitempty"`
	GitStatus     string            `json:"git_status,omitempty"`
	TopLevel      []string          `json:"top_level,omitempty"`
	DetectedStack []string          `json:"detected_stack,omitempty"`
	RelevantFiles map[string]string `json:"relevant_files,omitempty"`
}

type Collector struct {
	Redactor *redact.Redactor
}

func New(redactor *redact.Redactor) *Collector {
	return &Collector{Redactor: redactor}
}

func (c *Collector) CollectMinimal(ctx context.Context, runtimeState state.Runtime, stdin []byte) (Snapshot, error) {
	snapshot := Snapshot{
		System: map[string]any{
			"goos":      runtime.GOOS,
			"goarch":    runtime.GOARCH,
			"user":      os.Getenv("USER"),
			"shell":     os.Getenv("SHELL"),
			"cwd":       runtimeState.WorkingDir,
			"approval":  runtimeState.ApprovalMode,
			"read_only": runtimeState.ReadOnly,
		},
		Project: ProjectSnapshot{
			Name:        runtimeState.Project.Project.Name,
			Stack:       runtimeState.Project.Project.Stack,
			RunCommand:  runtimeState.Project.Project.RunCommand,
			TestCommand: runtimeState.Project.Project.TestCommand,
			Services:    runtimeState.Project.Project.Services,
		},
	}

	if len(stdin) > 0 {
		maxBytes := runtimeState.Config.Context.MaxSTDINBytes
		if maxBytes <= 0 || maxBytes > len(stdin) {
			maxBytes = len(stdin)
		}
		snapshot.STDIN = c.Redactor.Text(string(stdin[:maxBytes]))
	}

	return snapshot, nil
}

func (c *Collector) Collect(ctx context.Context, runtimeState state.Runtime, stdin []byte) (Snapshot, error) {
	snapshot := Snapshot{
		System: map[string]any{
			"goos":      runtime.GOOS,
			"goarch":    runtime.GOARCH,
			"user":      os.Getenv("USER"),
			"shell":     os.Getenv("SHELL"),
			"cwd":       runtimeState.WorkingDir,
			"approval":  runtimeState.ApprovalMode,
			"read_only": runtimeState.ReadOnly,
		},
		Project: ProjectSnapshot{
			Name:        runtimeState.Project.Project.Name,
			Stack:       runtimeState.Project.Project.Stack,
			RunCommand:  runtimeState.Project.Project.RunCommand,
			TestCommand: runtimeState.Project.Project.TestCommand,
			Services:    runtimeState.Project.Project.Services,
		},
	}

	c.collectSystem(ctx, runtimeState.Config, &snapshot)
	c.collectRepo(ctx, runtimeState.Config, &snapshot)

	if len(stdin) > 0 {
		maxBytes := runtimeState.Config.Context.MaxSTDINBytes
		if maxBytes <= 0 || maxBytes > len(stdin) {
			maxBytes = len(stdin)
		}
		snapshot.STDIN = c.Redactor.Text(string(stdin[:maxBytes]))
	}

	return snapshot, nil
}

func (s Snapshot) JSON() string {
	data, _ := json.MarshalIndent(s, "", "  ")
	return string(data)
}

func (c *Collector) collectSystem(ctx context.Context, cfg cfgpkg.Config, snapshot *Snapshot) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	commands := map[string][]string{
		"uname":            {"uname", "-srmo"},
		"os_release":       {"sh", "-lc", "test -f /etc/os-release && cat /etc/os-release || true"},
		"package_managers": {"sh", "-lc", "command -v apt-get dnf yum apk pacman brew 2>/dev/null || true"},
		"sudo":             {"sh", "-lc", "sudo -n true >/dev/null 2>&1 && echo yes || echo no"},
		"systemd":          {"sh", "-lc", "command -v systemctl >/dev/null 2>&1 && echo yes || echo no"},
		"docker":           {"sh", "-lc", "command -v docker >/dev/null 2>&1 && echo yes || echo no"},
		"disk":             {"sh", "-lc", "df -h . 2>/dev/null || true"},
	}

	for key, argv := range commands {
		out, _ := run(timeoutCtx, argv[0], argv[1:]...)
		out = strings.TrimSpace(out)
		if out == "" {
			continue
		}
		snapshot.System[key] = c.Redactor.Text(out)
	}
}

func (c *Collector) collectRepo(ctx context.Context, cfg cfgpkg.Config, snapshot *Snapshot) {
	root := findRepoRoot(ctx, snapshot.System["cwd"].(string))
	if root == "" {
		return
	}

	repo := RepoSnapshot{
		Detected:      true,
		Root:          root,
		RelevantFiles: map[string]string{},
	}

	if out, err := run(ctx, "git", "-C", root, "status", "--short", "--branch"); err == nil {
		repo.GitStatus = strings.TrimSpace(c.Redactor.Text(out))
	}

	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		repo.TopLevel = append(repo.TopLevel, entry.Name())
		if len(repo.TopLevel) >= cfg.Context.MaxListEntries && cfg.Context.MaxListEntries > 0 {
			break
		}
	}
	repo.DetectedStack = detectStack(repo.TopLevel)
	for _, path := range relevantFiles(root) {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		maxBytes := cfg.Context.MaxFileBytes
		if maxBytes > 0 && len(content) > maxBytes {
			content = content[:maxBytes]
		}
		repo.RelevantFiles[rel] = c.Redactor.Text(string(content))
	}
	snapshot.Repo = repo
}

func findRepoRoot(ctx context.Context, cwd string) string {
	if out, err := run(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel"); err == nil {
		return strings.TrimSpace(out)
	}
	return ""
}

func detectStack(entries []string) []string {
	stack := []string{}
	has := func(target string) bool {
		return slices.Contains(entries, target)
	}
	switch {
	case has("go.mod"):
		stack = append(stack, "go")
	case has("package.json"):
		stack = append(stack, "node")
	case has("pyproject.toml") || has("requirements.txt"):
		stack = append(stack, "python")
	case has("Gemfile"):
		stack = append(stack, "ruby")
	}
	if has("Dockerfile") || has("compose.yml") || has("docker-compose.yml") {
		stack = append(stack, "docker")
	}
	return stack
}

func relevantFiles(root string) []string {
	candidates := []string{
		"go.mod",
		"package.json",
		"pnpm-lock.yaml",
		"package-lock.json",
		"pyproject.toml",
		"requirements.txt",
		"Dockerfile",
		"docker-compose.yml",
		"compose.yml",
		"README.md",
		"README",
		".env.example",
		"Procfile",
		"Makefile",
	}
	results := []string{}
	for _, candidate := range candidates {
		path := filepath.Join(root, candidate)
		if _, err := os.Stat(path); err == nil {
			results = append(results, path)
		}
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".service") || strings.Contains(name, "compose") {
			results = append(results, path)
		}
		if len(results) >= 12 {
			return fs.SkipAll
		}
		return nil
	})
	return dedupe(results)
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dedupe(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}
