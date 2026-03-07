package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/richardson/lark/internal/checkpoints"
	"github.com/richardson/lark/internal/core"
	"github.com/richardson/lark/internal/patches"
	"github.com/richardson/lark/internal/policy"
	"github.com/richardson/lark/internal/redact"
	"github.com/richardson/lark/internal/search"
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
		stdout, stderr, code, err := e.runCommand(ctx, action)
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
			result.Error = err.Error()
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
	timeout := 30 * time.Second
	if action.TimeoutSec > 0 {
		timeout = time.Duration(action.TimeoutSec) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-lc", action.Command)
	cmd.Dir = e.WorkingDir
	if action.CWD != "" {
		cmd.Dir = action.CWD
	}

	stdoutBuf := &cappedBuffer{limit: e.MaxOutputBytes}
	stderrBuf := &cappedBuffer{limit: e.MaxOutputBytes}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	err := cmd.Run()
	stdout := e.Redactor.Text(stdoutBuf.String())
	stderr := e.Redactor.Text(stderrBuf.String())

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		return stdout, stderr, exitCode, err
	}

	return stdout, stderr, 0, nil
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
