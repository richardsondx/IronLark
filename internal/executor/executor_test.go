package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/search"
)

func TestRunCommandRetriesPipefailCommandWithBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}

	exec := testExecutor(t)
	stdout, stderr, exitCode, err := exec.runCommand(context.Background(), core.Action{
		Type:    core.ActionRunShell,
		Command: "set -euo pipefail; printf ok",
	})
	if err != nil {
		t.Fatalf("runCommand() error = %v, stderr=%q", err, stderr)
	}
	if exitCode != 0 {
		t.Fatalf("expected zero exit code, got %d", exitCode)
	}
	if strings.TrimSpace(stdout) != "ok" {
		t.Fatalf("expected bash fallback output, got %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr after fallback, got %q", stderr)
	}
}

func TestShouldRetryWithBash(t *testing.T) {
	if !shouldRetryWithBash("set -euo pipefail; echo hi", "sh: 1: set: Illegal option -o pipefail") {
		t.Fatal("expected pipefail fallback to trigger")
	}
	if shouldRetryWithBash("echo hi", "command not found") {
		t.Fatal("did not expect fallback for unrelated stderr")
	}
}

func TestExecuteEditFileReportsHelpfulUnifiedDiffError(t *testing.T) {
	exec := testExecutor(t)
	exec.PatchStore = patches.Store{Dir: filepath.Join(t.TempDir(), "patches")}
	exec.CheckpointStore = checkpoints.Store{Dir: filepath.Join(t.TempDir(), "checkpoints")}
	target := filepath.Join(exec.WorkingDir, "README.md")
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := exec.Execute(context.Background(), core.Action{
		Type:             core.ActionEditFile,
		Path:             target,
		Title:            "Update README",
		PatchUnifiedDiff: "--- README.md\n+++ README.md\n@@\n-hello\n+hello world\n",
	}, false)
	if err == nil {
		t.Fatal("expected invalid patch error")
	}
	if result.Summary != "the generated edit patch was invalid" {
		t.Fatalf("unexpected summary %q", result.Summary)
	}
	if !strings.Contains(result.Error, "valid unified diff hunk header") && !strings.Contains(result.Error, "ranged headers like @@ -12,3 +12,4 @@") {
		t.Fatalf("unexpected edit patch error %q", result.Error)
	}
}

func testExecutor(t *testing.T) *Executor {
	t.Helper()
	root := t.TempDir()
	classifier := policy.NewClassifier(cfgpkg.DefaultConfig().Security.ProtectedPaths)
	return &Executor{
		WorkingDir:     root,
		MaxOutputBytes: 64 * 1024,
		MaxListEntries: 50,
		MaxFileBytes:   64 * 1024,
		Redactor:       redact.New(nil),
		Classifier:     classifier,
		Searcher:       search.Searcher{UserAgent: "lark-term/test"},
	}
}
