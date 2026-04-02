package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/search"
	"github.com/richardsondx/IronLark/internal/taskruntime"
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

func TestExecuteWriteFileWritesContentAndMode(t *testing.T) {
	exec := testExecutor(t)
	exec.CheckpointStore = checkpoints.Store{Dir: filepath.Join(t.TempDir(), "checkpoints")}
	target := filepath.Join(exec.WorkingDir, "script.sh")

	result, err := exec.Execute(context.Background(), core.Action{
		Type:     core.ActionWriteFile,
		Path:     target,
		Title:    "Write script",
		Content:  "echo hello\n",
		FileMode: "0755",
	}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Summary == "" || result.CheckpointID == "" {
		t.Fatalf("expected summary and checkpoint, got %#v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "echo hello\n" {
		t.Fatalf("unexpected file contents %q", string(data))
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected mode 0755, got %v", info.Mode().Perm())
	}
}

func TestExecuteStreamEmitsShellOutputChunks(t *testing.T) {
	exec := testExecutor(t)
	var chunks []core.ActionOutputChunk

	result, err := exec.ExecuteStream(context.Background(), core.Action{
		ID:      "stream-shell",
		Type:    core.ActionRunShell,
		Command: "printf 'out-line\\n'; printf 'err-line\\n' 1>&2",
	}, false, func(chunk core.ActionOutputChunk) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected zero exit code, got %d", result.ExitCode)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected stdout and stderr chunks, got %#v", chunks)
	}
	if chunks[0].ActionID != "stream-shell" || chunks[1].ActionID != "stream-shell" {
		t.Fatalf("expected action id on chunks, got %#v", chunks)
	}
	streams := []core.ActionOutputStream{chunks[0].Stream, chunks[1].Stream}
	if !(containsStream(streams, core.ActionOutputStdout) && containsStream(streams, core.ActionOutputStderr)) {
		t.Fatalf("expected both stdout and stderr streams, got %#v", chunks)
	}
	if result.Handler != "shell.inline" {
		t.Fatalf("expected runtime handler to be recorded, got %#v", result)
	}
	if result.TaskID == "" {
		t.Fatalf("expected task id to be recorded, got %#v", result)
	}
}

func TestExecuteClassifiesShellTimeout(t *testing.T) {
	exec := testExecutor(t)
	result, err := exec.Execute(context.Background(), core.Action{
		ID:         "timeout-shell",
		Type:       core.ActionRunShell,
		Command:    "sleep 2",
		TimeoutSec: 1,
	}, false)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result.FailureKind != core.ShellFailureTimeout {
		t.Fatalf("expected timeout classification, got %#v", result)
	}
	if !result.TimedOut {
		t.Fatalf("expected timed_out to be true, got %#v", result)
	}
	if !result.BackgroundRecommended {
		t.Fatalf("expected background recommendation, got %#v", result)
	}
}

func TestIgnorableStreamReadError(t *testing.T) {
	cases := []error{
		io.EOF,
		os.ErrClosed,
		fmt.Errorf("read |0: file already closed"),
	}
	for _, err := range cases {
		if !isIgnorableStreamReadError(err) {
			t.Fatalf("expected %v to be ignored", err)
		}
	}
	if isIgnorableStreamReadError(fmt.Errorf("permission denied")) {
		t.Fatalf("expected unrelated error to remain visible")
	}
}

func TestExecuteWebSearchUsesProviderWhenAvailable(t *testing.T) {
	exec := testExecutor(t)
	exec.ProviderModel = "gpt-5-mini"
	exec.Provider = fakeWebSearchProvider{
		results: []string{"Harbor docs | https://harborframework.com/docs/agents | agent config"},
	}
	exec.Searcher = search.Searcher{WebSearchURL: "://bad-url"}

	result, err := exec.Execute(context.Background(), core.Action{
		Type:  core.ActionWebSearch,
		Query: "site:harborframework.com/docs Agents BaseEnvironment Installed agents",
	}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "Harbor docs") {
		t.Fatalf("unexpected stdout %q", result.Stdout)
	}
}

func TestExecuteWebSearchFallsBackWhenProviderDoesNotSupportIt(t *testing.T) {
	exec := testExecutor(t)
	exec.Provider = fakeWebSearchProvider{err: provider.ErrWebSearchUnsupported}
	exec.Searcher = search.Searcher{HTTPClient: httpClientForTestSearch(t), WebSearchURL: testSearchServer(t)}

	result, err := exec.Execute(context.Background(), core.Action{
		Type:  core.ActionWebSearch,
		Query: "example",
	}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "https://example.com/doc") {
		t.Fatalf("unexpected stdout %q", result.Stdout)
	}
}

func TestExecuteWebSearchFallsBackWhenProviderTimesOut(t *testing.T) {
	exec := testExecutor(t)
	exec.Provider = fakeWebSearchProvider{err: fmt.Errorf("provider request failed: %w (Client.Timeout exceeded while awaiting headers)", context.DeadlineExceeded)}
	exec.Searcher = search.Searcher{HTTPClient: httpClientForTestSearch(t), WebSearchURL: testSearchServer(t)}

	result, err := exec.Execute(context.Background(), core.Action{
		Type:  core.ActionWebSearch,
		Query: "example",
	}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "https://example.com/doc") {
		t.Fatalf("unexpected stdout %q", result.Stdout)
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
		TaskStore:      taskruntime.Store{Dir: filepath.Join(root, "tasks")},
	}
}

type fakeWebSearchProvider struct {
	results []string
	err     error
}

type fakeOpsStarter struct {
	watcherQuery  string
	recoveryGoal  string
	executable    string
	watcherResult OpsLaunchResult
	recoveryResult OpsLaunchResult
}

func (f fakeWebSearchProvider) WebSearch(ctx context.Context, req provider.SearchRequest) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]string(nil), f.results...), nil
}

func (f *fakeOpsStarter) startWatcher(_ context.Context, query, executable string) (OpsLaunchResult, error) {
	f.watcherQuery = query
	f.executable = executable
	if f.watcherResult.ID == "" {
		f.watcherResult = OpsLaunchResult{ID: "watch-1", Target: "nginx", PID: 123}
	}
	return f.watcherResult, nil
}

func (f *fakeOpsStarter) startRecovery(_ context.Context, goal, executable string) (OpsLaunchResult, error) {
	f.recoveryGoal = goal
	f.executable = executable
	if f.recoveryResult.ID == "" {
		f.recoveryResult = OpsLaunchResult{ID: "run-1", Target: "nginx", PID: 456}
	}
	return f.recoveryResult, nil
}

func testSearchServer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a class="result__a" href="https://example.com/doc">Example Doc</a><div class="result__snippet">Search snippet</div></body></html>`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func httpClientForTestSearch(t *testing.T) *http.Client {
	t.Helper()
	return http.DefaultClient
}

func containsStream(streams []core.ActionOutputStream, target core.ActionOutputStream) bool {
	for _, stream := range streams {
		if stream == target {
			return true
		}
	}
	return false
}

func TestExecuteStartWatcherUsesOpsRuntime(t *testing.T) {
	exec := testExecutor(t)
	starter := &fakeOpsStarter{
		watcherResult: OpsLaunchResult{ID: "watch-1", Target: "nginx", PID: 321, ObserveOnly: true},
	}
	exec.StartWatcher = starter.startWatcher

	result, err := exec.Execute(context.Background(), core.Action{
		ID:     "watch-nginx",
		Type:   core.ActionStartWatcher,
		Query:  "nginx",
		Reason: "Watch nginx while I am away",
	}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if starter.watcherQuery != "nginx" {
		t.Fatalf("expected watcher query to reach runtime, got %q", starter.watcherQuery)
	}
	if result.BackgroundRunID != "watch-1" || result.Handler != "ops.watch" {
		t.Fatalf("expected ops watcher runtime metadata, got %#v", result)
	}
	if !strings.Contains(result.Summary, "observe-only mode") {
		t.Fatalf("expected observe-only summary, got %#v", result)
	}
}

func TestExecuteStartRecoveryUsesOpsRuntime(t *testing.T) {
	exec := testExecutor(t)
	starter := &fakeOpsStarter{
		recoveryResult: OpsLaunchResult{ID: "run-1", Target: "nginx", PID: 654},
	}
	exec.StartRecovery = starter.startRecovery

	result, err := exec.Execute(context.Background(), core.Action{
		ID:     "recover-nginx",
		Type:   core.ActionStartRecovery,
		Query:  "nginx",
		Reason: "Recover nginx until stable",
	}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if starter.recoveryGoal != "nginx" {
		t.Fatalf("expected recovery goal to reach runtime, got %q", starter.recoveryGoal)
	}
	if result.BackgroundRunID != "run-1" || result.Handler != "ops.recover" {
		t.Fatalf("expected ops recovery runtime metadata, got %#v", result)
	}
	if !strings.Contains(result.Summary, "started recovery") {
		t.Fatalf("expected recovery summary, got %#v", result)
	}
}
