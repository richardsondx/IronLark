package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/provider"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/search"
)

type Executor struct {
	WorkingDir                  string
	MaxOutputBytes              int
	MaxListEntries              int
	MaxFileBytes                int
	Redactor                    *redact.Redactor
	Classifier                  *policy.Classifier
	PatchStore                  patches.Store
	CheckpointStore             checkpoints.Store
	Searcher                    search.Searcher
	RuleURLs                    []string
	SemanticMaxFiles            int
	SemanticChunkLines          int
	WebSearchResults            int
	DefaultShellTimeoutSec      int
	ShellStallWindowSec         int
	AutoBackgroundLongRuns      bool
	LongRunHeuristicsEnabled    bool
	DurableShellMaxRuntimeSec   int
	DurableShellLogPreviewBytes int
	ProviderModel               string
	Provider                    interface {
		WebSearch(ctx context.Context, req provider.SearchRequest) ([]string, error)
	}
	OpsFetcher interface {
		Fetch(query string, since time.Time, limit int) (string, error)
	}
}

type shellRunResult struct {
	stdout         string
	stderr         string
	exitCode       int
	err            error
	failureKind    core.ShellFailureKind
	timedOut       bool
	killedBySignal int
	retryable      bool
}

func (e *Executor) Preview(action core.Action, readOnly bool) (core.RiskReport, error) {
	return e.Classifier.Classify(action, readOnly)
}

func (e *Executor) Execute(ctx context.Context, action core.Action, readOnly bool) (core.ActionResult, error) {
	return e.ExecuteStream(ctx, action, readOnly, nil)
}

func (e *Executor) ExecuteStream(ctx context.Context, action core.Action, readOnly bool, onChunk func(core.ActionOutputChunk)) (core.ActionResult, error) {
	startedAt := time.Now().UTC()
	report, err := e.Classifier.Classify(action, readOnly)
	if err != nil {
		result := core.ActionResult{
			Action:    action,
			Risk:      report,
			StartedAt: startedAt,
			Error:     err.Error(),
		}
		finalize(&result, startedAt)
		return result, err
	}

	if readOnly && report.Level != core.RiskLow {
		result := core.ActionResult{
			Action:    action,
			Risk:      report,
			Skipped:   true,
			Summary:   "blocked by read-only mode",
			StartedAt: startedAt,
		}
		finalize(&result, startedAt)
		return result, nil
	}

	result := core.ActionResult{
		Action:    action,
		Risk:      report,
		Approved:  true,
		StartedAt: startedAt,
	}
	switch action.Type {
	case core.ActionRunShell:
		runResult := e.runCommandStream(ctx, action, onChunk)
		result.Stdout = runResult.stdout
		result.Stderr = runResult.stderr
		result.ExitCode = runResult.exitCode
		result.FailureKind = runResult.failureKind
		result.TimedOut = runResult.timedOut
		result.KilledBySignal = runResult.killedBySignal
		result.Retryable = runResult.retryable
		if runResult.failureKind == core.ShellFailureTimeout || runResult.failureKind == core.ShellFailureSignalKilled || runResult.failureKind == core.ShellFailureStalled {
			result.BackgroundRecommended = true
		}
		if runResult.err != nil {
			result.Error = runResult.err.Error()
			result.Summary = summarize(runResult.stderr, runResult.stdout)
			finalize(&result, startedAt)
			return result, runResult.err
		}
		result.Summary = summarize(runResult.stdout, runResult.stderr)
		finalize(&result, startedAt)
		return result, nil
	case core.ActionReadFiles:
		output, err := e.readFiles(action)
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.Stdout = output
		result.Summary = fmt.Sprintf("read %d file(s)", len(actionPaths(action)))
		finalize(&result, startedAt)
		return result, nil
	case core.ActionListDir:
		entries, err := os.ReadDir(action.Path)
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		lines := []string{}
		for _, entry := range entries {
			suffix := ""
			if entry.IsDir() {
				suffix = "/"
			}
			lines = append(lines, entry.Name()+suffix)
			if e.MaxListEntries > 0 && len(lines) >= e.MaxListEntries {
				break
			}
		}
		result.Stdout = strings.Join(lines, "\n")
		result.Summary = fmt.Sprintf("listed %s", action.Path)
		finalize(&result, startedAt)
		return result, nil
	case core.ActionSearchFiles:
		matches, err := e.Searcher.SearchFiles(ctx, firstNonEmpty(action.Path, e.WorkingDir), firstNonEmpty(action.Query, action.Pattern), action.Glob, search.Options{
			MaxResults:     e.MaxListEntries,
			MaxFileBytes:   e.MaxFileBytes,
			MaxOutputBytes: e.MaxOutputBytes,
		})
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.Stdout = strings.Join(matches, "\n")
		result.Summary = fmt.Sprintf("searched %s", firstNonEmpty(action.Path, e.WorkingDir))
		finalize(&result, startedAt)
		return result, nil
	case core.ActionSemanticSearch:
		matches, err := e.Searcher.SemanticSearch(firstNonEmpty(action.Path, e.WorkingDir), firstNonEmpty(action.Query, action.Pattern), search.Options{
			MaxResults:   e.MaxListEntries,
			MaxFileBytes: e.MaxFileBytes,
			MaxFiles:     e.SemanticMaxFiles,
			ChunkLines:   e.SemanticChunkLines,
		})
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.Stdout = strings.Join(matches, "\n")
		result.Summary = fmt.Sprintf("semantic search in %s", firstNonEmpty(action.Path, e.WorkingDir))
		finalize(&result, startedAt)
		return result, nil
	case core.ActionEditFile:
		paths := actionPaths(action)
		checkpoint, err := e.CheckpointStore.Create(paths, firstNonEmpty(action.Reason, action.Title))
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		record, err := e.PatchStore.Apply(firstNonEmpty(action.Path, firstPath(paths)), action.PatchUnifiedDiff)
		if err != nil {
			result.Error = explainEditPatchError(err)
			result.Summary = "the generated edit patch was invalid"
			finalize(&result, startedAt)
			return result, err
		}
		result.CheckpointID = checkpoint.ID
		result.PatchID = record.ID
		result.BackupPath = record.BackupPath
		result.Summary = fmt.Sprintf("patched %s", firstNonEmpty(action.Path, firstPath(paths)))
		finalize(&result, startedAt)
		return result, nil
	case core.ActionWriteFile:
		paths := actionPaths(action)
		target := firstNonEmpty(action.Path, firstPath(paths))
		if target == "" {
			result.Error = "write_file requires a path"
			finalize(&result, startedAt)
			return result, errors.New(result.Error)
		}
		checkpoint, err := e.CheckpointStore.Create([]string{target}, firstNonEmpty(action.Reason, action.Title))
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		mode, err := resolveWriteFileMode(target, action.FileMode)
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		if err := os.WriteFile(target, []byte(action.Content), mode); err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		if action.FileMode != "" {
			_ = os.Chmod(target, mode)
		}
		result.CheckpointID = checkpoint.ID
		result.Summary = fmt.Sprintf("wrote %s", target)
		finalize(&result, startedAt)
		return result, nil
	case core.ActionWebSearch:
		query := firstNonEmpty(action.Query, action.Pattern, action.Reason)
		results, err := e.providerWebSearch(ctx, query)
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.Stdout = strings.Join(results, "\n")
		result.Summary = fmt.Sprintf("web search for %q", query)
		finalize(&result, startedAt)
		return result, nil
	case core.ActionFetchOps:
		if e.OpsFetcher == nil {
			err := fmt.Errorf("ops memory is unavailable")
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		output, err := e.OpsFetcher.Fetch(firstNonEmpty(action.Query, action.Pattern, action.Reason), time.Time{}, e.MaxListEntries)
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.Stdout = output
		result.Summary = "fetched operational history"
		finalize(&result, startedAt)
		return result, nil
	case core.ActionFetchRules:
		results, err := e.Searcher.FetchRules(ctx, firstNonEmpty(action.CWD, e.WorkingDir), firstNonEmpty(action.Query, action.Pattern, action.Reason), e.RuleURLs, search.Options{
			MaxResults: e.MaxListEntries,
		})
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.Stdout = strings.Join(results, "\n")
		result.Summary = "fetched relevant rules"
		finalize(&result, startedAt)
		return result, nil
	case core.ActionCheckpoint:
		record, err := e.CheckpointStore.Create(actionPaths(action), firstNonEmpty(action.Reason, action.Title))
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.CheckpointID = record.ID
		result.Summary = fmt.Sprintf("checkpointed %d file(s)", len(record.Files))
		finalize(&result, startedAt)
		return result, nil
	case core.ActionAskUser:
		result.Summary = action.Reason
		finalize(&result, startedAt)
		return result, nil
	case core.ActionFinish:
		result.Summary = "finished"
		finalize(&result, startedAt)
		return result, nil
	default:
		err := fmt.Errorf("unsupported action type %s", action.Type)
		result.Error = err.Error()
		finalize(&result, startedAt)
		return result, err
	}
}

func (e *Executor) readFiles(action core.Action) (string, error) {
	paths := actionPaths(action)
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if e.MaxFileBytes > 0 && len(data) > e.MaxFileBytes {
			data = data[:e.MaxFileBytes]
		}
		parts = append(parts, fmt.Sprintf("==> %s <==\n%s", path, e.Redactor.Text(string(data))))
	}
	return strings.Join(parts, "\n\n"), nil
}

func (e *Executor) runCommand(ctx context.Context, action core.Action) (string, string, int, error) {
	result := e.runCommandStream(ctx, action, nil)
	return result.stdout, result.stderr, result.exitCode, result.err
}

func (e *Executor) runCommandStream(ctx context.Context, action core.Action, onChunk func(core.ActionOutputChunk)) shellRunResult {
	timeout := 30 * time.Second
	if e.DefaultShellTimeoutSec > 0 {
		timeout = time.Duration(e.DefaultShellTimeoutSec) * time.Second
	}
	if action.TimeoutSec > 0 {
		timeout = time.Duration(action.TimeoutSec) * time.Second
	}
	runCtx, cancel := context.WithTimeoutCause(ctx, timeout, context.DeadlineExceeded)
	defer cancel()

	dir := e.WorkingDir
	if action.CWD != "" {
		dir = action.CWD
	}

	result := e.runShellStream(runCtx, dir, "sh", action, onChunk)
	if result.err == nil {
		return result
	}
	if shouldRetryWithBash(action.Command, result.stderr) {
		if _, lookErr := exec.LookPath("bash"); lookErr == nil {
			return e.runShellStream(runCtx, dir, "bash", action, onChunk)
		}
	}
	return result
}

func (e *Executor) runShell(ctx context.Context, dir, shell, command string) (string, string, int, error) {
	result := e.runShellStream(ctx, dir, shell, core.Action{Command: command}, nil)
	return result.stdout, result.stderr, result.exitCode, result.err
}

func (e *Executor) runShellStream(ctx context.Context, dir, shell string, action core.Action, onChunk func(core.ActionOutputChunk)) shellRunResult {
	cmd := exec.CommandContext(ctx, shell, "-lc", action.Command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutBuf := &cappedBuffer{limit: e.MaxOutputBytes}
	stderrBuf := &cappedBuffer{limit: e.MaxOutputBytes}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return shellRunResult{exitCode: -1, err: err, failureKind: core.ShellFailureStartup}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return shellRunResult{exitCode: -1, err: err, failureKind: core.ShellFailureStartup}
	}
	if err := cmd.Start(); err != nil {
		return shellRunResult{exitCode: -1, err: err, failureKind: core.ShellFailureStartup}
	}

	stallWindow := time.Duration(e.ShellStallWindowSec) * time.Second
	var (
		activityMu   sync.Mutex
		lastActivity = time.Now()
		stallErr     error
	)
	markActivity := func() {
		activityMu.Lock()
		lastActivity = time.Now()
		activityMu.Unlock()
	}
	var stallCancel context.CancelFunc
	if stallWindow > 0 {
		stallCtx, cancel := context.WithCancel(context.Background())
		stallCancel = cancel
		go func() {
			ticker := time.NewTicker(minDuration(5*time.Second, stallWindow))
			defer ticker.Stop()
			for {
				select {
				case <-stallCtx.Done():
					return
				case <-ticker.C:
					activityMu.Lock()
					idle := time.Since(lastActivity)
					activityMu.Unlock()
					if idle < stallWindow {
						continue
					}
					stallErr = fmt.Errorf("command stalled without output for %s", stallWindow)
					_ = killProcessGroup(cmd.Process)
					cancel()
					return
				}
			}
		}()
		defer stallCancel()
	}

	type streamResult struct {
		stream core.ActionOutputStream
		err    error
	}
	streamDone := make(chan streamResult, 2)
	go func() {
		streamDone <- streamResult{
			stream: core.ActionOutputStdout,
			err:    e.captureStream(stdoutBuf, stdoutPipe, action.ID, core.ActionOutputStdout, onChunk, markActivity),
		}
	}()
	go func() {
		streamDone <- streamResult{
			stream: core.ActionOutputStderr,
			err:    e.captureStream(stderrBuf, stderrPipe, action.ID, core.ActionOutputStderr, onChunk, markActivity),
		}
	}()

	waitErr := cmd.Wait()
	var streamErr error
	for i := 0; i < 2; i++ {
		result := <-streamDone
		if streamErr == nil && result.err != nil && !isIgnorableStreamReadError(result.err) {
			streamErr = result.err
		}
	}
	stdout := e.Redactor.Text(stdoutBuf.String())
	stderr := e.Redactor.Text(stderrBuf.String())
	if streamErr != nil {
		return shellRunResult{stdout: stdout, stderr: stderr, exitCode: -1, err: streamErr, failureKind: core.ShellFailureStream, retryable: true}
	}

	if waitErr != nil {
		result := shellRunResult{stdout: stdout, stderr: stderr, exitCode: -1, err: waitErr, failureKind: core.ShellFailureUnknown}
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			result.exitCode = exitErr.ExitCode()
			result.failureKind = core.ShellFailureNonZeroExit
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				result.killedBySignal = int(status.Signal())
				result.failureKind = core.ShellFailureSignalKilled
				result.retryable = true
			}
		}
		if stallErr != nil {
			result.err = stallErr
			result.failureKind = core.ShellFailureStalled
			result.retryable = true
		} else if errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.timedOut = true
			result.failureKind = core.ShellFailureTimeout
			result.retryable = true
		}
		return result
	}

	return shellRunResult{stdout: stdout, stderr: stderr, exitCode: 0, err: nil}
}

func isIgnorableStreamReadError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF || errors.Is(err, fs.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file already closed") ||
		strings.Contains(message, "closed pipe")
}

func (e *Executor) captureStream(buf *cappedBuffer, reader io.Reader, actionID string, stream core.ActionOutputStream, onChunk func(core.ActionOutputChunk), onActivity func()) error {
	streamReader := bufio.NewReader(reader)
	for {
		line, err := streamReader.ReadString('\n')
		if len(line) > 0 {
			if onActivity != nil {
				onActivity()
			}
			_, _ = buf.Write([]byte(line))
			if onChunk != nil {
				text := strings.TrimRight(e.Redactor.Text(line), "\n")
				if strings.TrimSpace(text) != "" {
					onChunk(core.ActionOutputChunk{
						ActionID: actionID,
						Stream:   stream,
						Text:     text,
					})
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func killProcessGroup(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return syscall.Kill(-proc.Pid, syscall.SIGKILL)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func shouldRetryWithBash(command, stderr string) bool {
	normalizedCommand := strings.ToLower(command)
	normalizedErr := strings.ToLower(stderr)
	return strings.Contains(normalizedCommand, "pipefail") &&
		(strings.Contains(normalizedErr, "illegal option -o pipefail") ||
			strings.Contains(normalizedErr, "bad option: -o pipefail") ||
			strings.Contains(normalizedErr, "invalid option") && strings.Contains(normalizedErr, "pipefail"))
}

func (e *Executor) grepFiles(root, pattern string) ([]string, error) {
	matches := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if e.MaxFileBytes > 0 && len(data) > e.MaxFileBytes {
			data = data[:e.MaxFileBytes]
		}
		lines := strings.Split(string(data), "\n")
		for idx, line := range lines {
			if strings.Contains(line, pattern) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", path, idx+1, e.Redactor.Text(line)))
				if e.MaxListEntries > 0 && len(matches) >= e.MaxListEntries {
					return io.EOF
				}
			}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	return matches, nil
}

type cappedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.limit <= 0 {
		return c.buf.Write(p)
	}
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}

func (e *Executor) providerWebSearch(ctx context.Context, query string) ([]string, error) {
	if e.Provider != nil {
		results, err := e.Provider.WebSearch(ctx, provider.SearchRequest{
			Model:      e.ProviderModel,
			Query:      query,
			MaxResults: e.WebSearchResults,
		})
		if err == nil {
			return results, nil
		}
		if !errors.Is(err, provider.ErrWebSearchUnsupported) {
			if !provider.IsRetryableWebSearchError(err) {
				return nil, err
			}
		}
	}
	return e.Searcher.WebSearch(ctx, query, search.Options{
		MaxResults: e.WebSearchResults,
	})
}

func summarize(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
	}
	return ""
}

func explainEditPatchError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	switch {
	case strings.Contains(message, "invalid unified diff hunk header"):
		return "The model generated an invalid unified diff hunk header. edit_file patches must use standard ranged headers like @@ -12,3 +12,4 @@. Consider using write_file for full-file rewrites."
	case strings.Contains(message, "patch does not contain any unified diff hunks"):
		return "The model generated an edit patch without any valid unified diff hunks. edit_file patches must include ---/+++ file headers and at least one @@ -old,+new @@ hunk. Consider using write_file for full-file rewrites."
	case strings.Contains(message, "standard unified diff"):
		return "The model generated an edit patch in the wrong format. edit_file only accepts standard unified diffs with ---/+++ file headers and @@ -old,+new @@ hunks. Consider using write_file for full-file rewrites."
	default:
		return message
	}
}

func resolveWriteFileMode(path, mode string) (os.FileMode, error) {
	if strings.TrimSpace(mode) != "" {
		parsed, err := parseFileMode(mode)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	}
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().Perm(), nil
	}
	if os.IsNotExist(err) {
		return 0o644, nil
	}
	return 0, err
}

func parseFileMode(raw string) (os.FileMode, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("file_mode is empty")
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "0o") {
		lower = strings.TrimPrefix(lower, "0o")
	}
	isOctal := true
	for _, r := range lower {
		if r < '0' || r > '7' {
			isOctal = false
			break
		}
	}
	base := 10
	if isOctal {
		base = 8
	}
	parsed, err := strconv.ParseUint(lower, base, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid file_mode %q", raw)
	}
	return os.FileMode(parsed), nil
}

func finalize(result *core.ActionResult, startedAt time.Time) {
	result.FinishedAt = time.Now().UTC()
	result.DurationMS = result.FinishedAt.Sub(startedAt).Milliseconds()
}

func actionPaths(action core.Action) []string {
	if len(action.Paths) > 0 {
		return action.Paths
	}
	if action.Path != "" {
		return []string{action.Path}
	}
	return nil
}

func firstPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
