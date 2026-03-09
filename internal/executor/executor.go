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
	"strings"
	"time"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/redact"
	"github.com/richardsondx/IronLark/internal/search"
)

type Executor struct {
	WorkingDir         string
	MaxOutputBytes     int
	MaxListEntries     int
	MaxFileBytes       int
	Redactor           *redact.Redactor
	Classifier         *policy.Classifier
	PatchStore         patches.Store
	CheckpointStore    checkpoints.Store
	Searcher           search.Searcher
	RuleURLs           []string
	SemanticMaxFiles   int
	SemanticChunkLines int
	WebSearchResults   int
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
		stdout, stderr, code, err := e.runCommandStream(ctx, action, onChunk)
		result.Stdout = stdout
		result.Stderr = stderr
		result.ExitCode = code
		if err != nil {
			result.Error = err.Error()
			result.Summary = summarize(stderr, stdout)
			finalize(&result, startedAt)
			return result, err
		}
		result.Summary = summarize(stdout, stderr)
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
	case core.ActionWebSearch:
		results, err := e.Searcher.WebSearch(ctx, firstNonEmpty(action.Query, action.Pattern, action.Reason), search.Options{
			MaxResults: e.WebSearchResults,
		})
		if err != nil {
			result.Error = err.Error()
			finalize(&result, startedAt)
			return result, err
		}
		result.Stdout = strings.Join(results, "\n")
		result.Summary = fmt.Sprintf("web search for %q", firstNonEmpty(action.Query, action.Pattern, action.Reason))
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
	return e.runCommandStream(ctx, action, nil)
}

func (e *Executor) runCommandStream(ctx context.Context, action core.Action, onChunk func(core.ActionOutputChunk)) (string, string, int, error) {
	timeout := 30 * time.Second
	if action.TimeoutSec > 0 {
		timeout = time.Duration(action.TimeoutSec) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dir := e.WorkingDir
	if action.CWD != "" {
		dir = action.CWD
	}

	stdout, stderr, exitCode, err := e.runShellStream(runCtx, dir, "sh", action, onChunk)
	if err == nil {
		return stdout, stderr, exitCode, nil
	}
	if shouldRetryWithBash(action.Command, stderr) {
		if _, lookErr := exec.LookPath("bash"); lookErr == nil {
			return e.runShellStream(runCtx, dir, "bash", action, onChunk)
		}
	}
	return stdout, stderr, exitCode, err
}

func (e *Executor) runShell(ctx context.Context, dir, shell, command string) (string, string, int, error) {
	return e.runShellStream(ctx, dir, shell, core.Action{Command: command}, nil)
}

func (e *Executor) runShellStream(ctx context.Context, dir, shell string, action core.Action, onChunk func(core.ActionOutputChunk)) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, shell, "-lc", action.Command)
	cmd.Dir = dir

	stdoutBuf := &cappedBuffer{limit: e.MaxOutputBytes}
	stderrBuf := &cappedBuffer{limit: e.MaxOutputBytes}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", -1, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", "", -1, err
	}
	if err := cmd.Start(); err != nil {
		return "", "", -1, err
	}

	type streamResult struct {
		stream core.ActionOutputStream
		err    error
	}
	streamDone := make(chan streamResult, 2)
	go func() {
		streamDone <- streamResult{
			stream: core.ActionOutputStdout,
			err:    e.captureStream(stdoutBuf, stdoutPipe, action.ID, core.ActionOutputStdout, onChunk),
		}
	}()
	go func() {
		streamDone <- streamResult{
			stream: core.ActionOutputStderr,
			err:    e.captureStream(stderrBuf, stderrPipe, action.ID, core.ActionOutputStderr, onChunk),
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
		return stdout, stderr, -1, streamErr
	}

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		return stdout, stderr, exitCode, waitErr
	}

	return stdout, stderr, 0, nil
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

func (e *Executor) captureStream(buf *cappedBuffer, reader io.Reader, actionID string, stream core.ActionOutputStream, onChunk func(core.ActionOutputChunk)) error {
	streamReader := bufio.NewReader(reader)
	for {
		line, err := streamReader.ReadString('\n')
		if len(line) > 0 {
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
		return "The model generated an invalid unified diff hunk header. edit_file patches must use standard ranged headers like @@ -12,3 +12,4 @@."
	case strings.Contains(message, "patch does not contain any unified diff hunks"):
		return "The model generated an edit patch without any valid unified diff hunks. edit_file patches must include ---/+++ file headers and at least one @@ -old,+new @@ hunk."
	case strings.Contains(message, "standard unified diff"):
		return "The model generated an edit patch in the wrong format. edit_file only accepts standard unified diffs with ---/+++ file headers and @@ -old,+new @@ hunks."
	default:
		return message
	}
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
