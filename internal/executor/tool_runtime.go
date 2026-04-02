package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/search"
	"github.com/richardsondx/IronLark/internal/taskruntime"
	"github.com/richardsondx/IronLark/internal/toolruntime"
)

func (e *Executor) ensureToolRuntime() {
	if e.ToolRuntime != nil {
		return
	}
	runtime := toolruntime.New()
	handlers := []toolruntime.Handler{
		toolHandler{name: "shell.inline", actionType: core.ActionRunShell, execute: e.executeRunShell},
		toolHandler{name: "files.read", actionType: core.ActionReadFiles, execute: e.executeReadFiles},
		toolHandler{name: "files.search", actionType: core.ActionSearchFiles, execute: e.executeSearchFiles},
		toolHandler{name: "files.patch", actionType: core.ActionEditFile, execute: e.executeEditFile},
		toolHandler{name: "ops.watch", actionType: core.ActionStartWatcher, execute: e.executeStartWatcher},
		toolHandler{name: "ops.recover", actionType: core.ActionStartRecovery, execute: e.executeStartRecovery},
	}
	for _, handler := range handlers {
		_ = runtime.Register(handler)
	}
	e.ToolRuntime = runtime
}

type toolHandler struct {
	name       string
	actionType core.ActionType
	execute    func(context.Context, core.Action, bool, func(core.ActionOutputChunk)) (core.ActionResult, error)
}

func (h toolHandler) ActionType() core.ActionType { return h.actionType }
func (h toolHandler) Name() string                { return h.name }
func (h toolHandler) Execute(ctx context.Context, action core.Action, readOnly bool, onChunk func(core.ActionOutputChunk)) (core.ActionResult, error) {
	return h.execute(ctx, action, readOnly, onChunk)
}

func (e *Executor) executeRunShell(ctx context.Context, action core.Action, _ bool, onChunk func(core.ActionOutputChunk)) (core.ActionResult, error) {
	runResult := e.runCommandStream(ctx, action, onChunk)
	result := core.ActionResult{
		Stdout:         runResult.stdout,
		Stderr:         runResult.stderr,
		ExitCode:       runResult.exitCode,
		FailureKind:    runResult.failureKind,
		TimedOut:       runResult.timedOut,
		KilledBySignal: runResult.killedBySignal,
		Retryable:      runResult.retryable,
	}
	if runResult.failureKind == core.ShellFailureTimeout || runResult.failureKind == core.ShellFailureSignalKilled || runResult.failureKind == core.ShellFailureStalled {
		result.BackgroundRecommended = true
	}
	if runResult.err != nil {
		result.Error = runResult.err.Error()
		result.Summary = summarize(runResult.stderr, runResult.stdout)
		return result, runResult.err
	}
	result.Summary = summarize(runResult.stdout, runResult.stderr)
	return result, nil
}

func (e *Executor) executeReadFiles(_ context.Context, action core.Action, _ bool, _ func(core.ActionOutputChunk)) (core.ActionResult, error) {
	normalized, err := e.normalizeActionPaths(action)
	if err != nil {
		return core.ActionResult{Error: err.Error()}, err
	}
	output, err := e.readFiles(normalized)
	if err != nil {
		return core.ActionResult{Error: err.Error()}, err
	}
	return core.ActionResult{
		Action:  normalized,
		Stdout:  output,
		Summary: fmt.Sprintf("read %d file(s)", len(actionPaths(normalized))),
	}, nil
}

func (e *Executor) executeSearchFiles(ctx context.Context, action core.Action, _ bool, _ func(core.ActionOutputChunk)) (core.ActionResult, error) {
	normalized, err := e.normalizeActionSearchPath(action)
	if err != nil {
		return core.ActionResult{Error: err.Error()}, err
	}
	matches, err := e.Searcher.SearchFiles(ctx, firstNonEmpty(normalized.Path, e.WorkingDir), firstNonEmpty(normalized.Query, normalized.Pattern), normalized.Glob, search.Options{
		MaxResults:     e.MaxListEntries,
		MaxFileBytes:   e.MaxFileBytes,
		MaxOutputBytes: e.MaxOutputBytes,
	})
	if err != nil {
		return core.ActionResult{Action: normalized, Error: err.Error()}, err
	}
	return core.ActionResult{
		Action:  normalized,
		Stdout:  strings.Join(matches, "\n"),
		Summary: fmt.Sprintf("searched %s", firstNonEmpty(normalized.Path, e.WorkingDir)),
	}, nil
}

func (e *Executor) executeEditFile(_ context.Context, action core.Action, _ bool, _ func(core.ActionOutputChunk)) (core.ActionResult, error) {
	normalized, err := e.normalizeActionPaths(action)
	if err != nil {
		return core.ActionResult{Error: err.Error()}, err
	}
	paths := actionPaths(normalized)
	checkpoint, err := e.CheckpointStore.Create(paths, firstNonEmpty(normalized.Reason, normalized.Title))
	if err != nil {
		return core.ActionResult{Action: normalized, Error: err.Error()}, err
	}
	record, err := e.PatchStore.Apply(firstNonEmpty(normalized.Path, firstPath(paths)), normalized.PatchUnifiedDiff)
	if err != nil {
		return core.ActionResult{
			Action:  normalized,
			Error:   explainEditPatchError(err),
			Summary: "the generated edit patch was invalid",
		}, err
	}
	return core.ActionResult{
		Action:       normalized,
		CheckpointID: checkpoint.ID,
		PatchID:      record.ID,
		BackupPath:   record.BackupPath,
		Summary:      fmt.Sprintf("patched %s", firstNonEmpty(normalized.Path, firstPath(paths))),
	}, nil
}

func (e *Executor) executeStartWatcher(ctx context.Context, action core.Action, _ bool, _ func(core.ActionOutputChunk)) (core.ActionResult, error) {
	if e.StartWatcher == nil {
		err := fmt.Errorf("watcher runtime is unavailable")
		return core.ActionResult{Action: action, Error: err.Error()}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return core.ActionResult{Action: action, Error: err.Error()}, err
	}
	query := firstNonEmpty(action.Query, action.Pattern, action.Reason, action.Title)
	started, err := e.StartWatcher(ctx, query, executable)
	if err != nil {
		return core.ActionResult{Action: action, Error: err.Error()}, err
	}
	summary := started.Summary
	if strings.TrimSpace(summary) == "" {
		summary = fmt.Sprintf("started watcher %s for %s", started.ID, firstNonEmpty(started.Target, query))
		if started.ObserveOnly {
			summary += " in observe-only mode"
		}
	}
	return core.ActionResult{
		Action:          action,
		Summary:         summary,
		BackgroundRunID: started.ID,
		Stdout:          firstNonEmpty(started.Target, query),
	}, nil
}

func (e *Executor) executeStartRecovery(ctx context.Context, action core.Action, _ bool, _ func(core.ActionOutputChunk)) (core.ActionResult, error) {
	if e.StartRecovery == nil {
		err := fmt.Errorf("recovery runtime is unavailable")
		return core.ActionResult{Action: action, Error: err.Error()}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return core.ActionResult{Action: action, Error: err.Error()}, err
	}
	goal := firstNonEmpty(action.Query, action.Pattern, action.Reason, action.Title)
	started, err := e.StartRecovery(ctx, goal, executable)
	if err != nil {
		return core.ActionResult{Action: action, Error: err.Error()}, err
	}
	summary := started.Summary
	if strings.TrimSpace(summary) == "" {
		summary = fmt.Sprintf("started recovery %s for %s", started.ID, firstNonEmpty(started.Target, goal))
	}
	return core.ActionResult{
		Action:          action,
		Summary:         summary,
		BackgroundRunID: started.ID,
		Stdout:          firstNonEmpty(started.Target, goal),
	}, nil
}

func (e *Executor) executeRuntimeAction(ctx context.Context, action core.Action, readOnly bool, onChunk func(core.ActionOutputChunk), base core.ActionResult, startedAt time.Time) (core.ActionResult, error) {
	handlerName := e.ToolRuntime.HandlerName(action.Type)
	task := taskruntime.NewActionRecord(action, handlerName)
	task.State = taskruntime.StateRunning
	_ = e.TaskStore.Save(task)

	result, err := e.ToolRuntime.Execute(ctx, action, readOnly, onChunk)
	if result.Action.Type == "" {
		result.Action = action
	}
	result.Risk = base.Risk
	result.Approved = base.Approved
	result.TaskID = task.ID
	result.Handler = handlerName
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	finalize(&result, startedAt)

	task.State = taskStateFromResult(result, err)
	task.Summary = firstNonEmpty(result.Summary, result.Error)
	task.Error = result.Error
	task.BackgroundRunID = result.BackgroundRunID
	task.FinishedAt = result.FinishedAt
	_ = e.TaskStore.Save(task)
	return result, err
}

func taskStateFromResult(result core.ActionResult, err error) taskruntime.State {
	if result.Skipped {
		return taskruntime.StateSkipped
	}
	if strings.TrimSpace(result.BackgroundRunID) != "" {
		return taskruntime.StateRunning
	}
	if err != nil || strings.TrimSpace(result.Error) != "" {
		return taskruntime.StateFailed
	}
	return taskruntime.StateCompleted
}

func (e *Executor) normalizeActionSearchPath(action core.Action) (core.Action, error) {
	if strings.TrimSpace(action.Path) == "" {
		action.Path = e.WorkingDir
		return action, nil
	}
	return e.normalizeActionPaths(action)
}

func (e *Executor) normalizeActionPaths(action core.Action) (core.Action, error) {
	normalize := func(raw string) (string, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", nil
		}
		if !filepath.IsAbs(raw) {
			raw = filepath.Join(e.WorkingDir, raw)
		}
		clean := filepath.Clean(raw)
		if clean == "." || clean == "/" {
			return clean, nil
		}
		info, err := os.Lstat(clean)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to operate on symlink path %s", clean)
		}
		return clean, nil
	}

	var err error
	action.Path, err = normalize(action.Path)
	if err != nil {
		return action, err
	}
	for idx, path := range action.Paths {
		action.Paths[idx], err = normalize(path)
		if err != nil {
			return action, err
		}
	}
	if strings.TrimSpace(action.CWD) != "" {
		action.CWD, err = normalize(action.CWD)
		if err != nil {
			return action, err
		}
	}
	return action, nil
}
