package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/executor"
	"github.com/richardsondx/IronLark/internal/graph"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/state"
)

type RuntimeDeps struct {
	Runtime    state.Runtime
	Graph      *graph.Manager
	Executor   *executor.Executor
	Policy     policy.Store
	Host       string
	WorkingDir string
}

func (m *Manager) StartWatcher(ctx context.Context, deps RuntimeDeps, query, executable string) (Watcher, int, error) {
	snapshot, _, err := deps.Graph.EnsureFresh(ctx, graph.ModeLight)
	if err != nil {
		return Watcher{}, 0, err
	}
	entity, err := ResolveEntity(snapshot, query)
	if err != nil {
		return Watcher{}, 0, err
	}
	id := fmt.Sprintf("watch-%d", time.Now().UTC().UnixNano())
	watcher := Watcher{
		ID:                 id,
		Query:              query,
		Host:               deps.Host,
		CWD:                deps.WorkingDir,
		Entity:             entity,
		ObserveOnly:        entity.ObserveOnly || !hasStrongProbe(entity),
		State:              "healthy",
		IntervalSec:        30,
		FailureThreshold:   3,
		StabilityWindowSec: 300,
		CooldownSec:        900,
		RestartBudget:      1,
		Baseline: Baseline{
			CapturedAt:    time.Now().UTC(),
			Entity:        entity,
			ExpectedPorts: append([]int(nil), entity.ExpectedPorts...),
			Manager:       entity.Manager,
		},
		Provider:     deps.Runtime.ProviderName,
		Model:        deps.Runtime.Model,
		ApprovalMode: deps.Runtime.ApprovalMode,
	}
	if watcher.ObserveOnly {
		watcher.State = "disabled"
		watcher.LastSummary = "created in observe-only mode because no strong active probe was available"
	}
	if err := m.SaveWatcher(watcher); err != nil {
		return Watcher{}, 0, err
	}
	pid, err := launchDetached(executable, []string{"watch", "__run", "--id", watcher.ID}, deps.WorkingDir)
	if err != nil {
		return Watcher{}, 0, err
	}
	return watcher, pid, nil
}

func (m *Manager) StartRecovery(ctx context.Context, deps RuntimeDeps, goal, executable string) (RecoverySpec, int, error) {
	snapshot, _, err := deps.Graph.EnsureFresh(ctx, graph.ModeLight)
	if err != nil {
		return RecoverySpec{}, 0, err
	}
	entity, err := ResolveEntity(snapshot, goal)
	if err != nil {
		return RecoverySpec{}, 0, err
	}
	id := fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
	spec := RecoverySpec{
		ID:                 id,
		Goal:               goal,
		Query:              goal,
		Host:               deps.Host,
		CWD:                deps.WorkingDir,
		Entity:             entity,
		AllowedActions:     []string{"restart_service", "restart_container"},
		RetryBudget:        3,
		StabilityWindowSec: 300,
		ApprovalMode:       deps.Runtime.ApprovalMode,
		Provider:           deps.Runtime.ProviderName,
		Model:              deps.Runtime.Model,
		CreatedAt:          time.Now().UTC(),
	}
	state := RecoveryState{
		Phase:     "initialize",
		StartedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := m.SaveRecoverySpec(spec); err != nil {
		return RecoverySpec{}, 0, err
	}
	if err := m.SaveRecoveryState(spec.ID, state); err != nil {
		return RecoverySpec{}, 0, err
	}
	progress := fmt.Sprintf("# Recovery %s\n\nGoal: %s\nTarget: %s\n\n- initialize: pending\n- diagnose: pending\n- remediate: pending\n- verify: pending\n- stabilize: pending\n", spec.ID, goal, spec.Entity.DisplayName)
	if err := m.WriteRecoveryProgress(spec.ID, progress); err != nil {
		return RecoverySpec{}, 0, err
	}
	pid, err := launchDetached(executable, []string{"recover", "__run", "--id", spec.ID}, deps.WorkingDir)
	if err != nil {
		return RecoverySpec{}, 0, err
	}
	return spec, pid, nil
}

func (m *Manager) StartShellRun(ctx context.Context, deps RuntimeDeps, action core.Action, promotion ShellPromotionReason, executable string) (ShellRunStatus, int, error) {
	_ = ctx
	id := fmt.Sprintf("shell-%d", time.Now().UTC().UnixNano())
	spec := ShellRunSpec{
		ID:              id,
		Command:         action.Command,
		CWD:             firstNonEmptyShell(action.CWD, deps.WorkingDir),
		Shell:           "sh",
		TimeoutSec:      action.TimeoutSec,
		StallWindowSec:  deps.Runtime.Config.Tools.ShellStallWindowSec,
		MaxRuntimeSec:   deps.Runtime.Config.Tools.DurableShellMaxRuntimeSec,
		PromotionReason: promotion,
		Host:            deps.Host,
		ApprovalMode:    deps.Runtime.ApprovalMode,
		Provider:        deps.Runtime.ProviderName,
		Model:           deps.Runtime.Model,
		CreatedAt:       time.Now().UTC(),
	}
	state := ShellRunState{
		State:         "starting",
		AttemptCount:  1,
		StdoutLogPath: m.ShellRunStdoutPath(id),
		StderrLogPath: m.ShellRunStderrPath(id),
		StartedAt:     time.Now().UTC(),
	}
	if err := m.SaveShellRunSpec(spec); err != nil {
		return ShellRunStatus{}, 0, err
	}
	if err := m.SaveShellRunState(spec.ID, state); err != nil {
		return ShellRunStatus{}, 0, err
	}
	pid, err := launchDetached(executable, []string{"run", "__exec", "--id", spec.ID}, spec.CWD)
	if err != nil {
		return ShellRunStatus{}, 0, err
	}
	return ShellRunStatus{Spec: spec, State: state}, pid, nil
}

func (m *Manager) RunWatcher(ctx context.Context, deps RuntimeDeps, id string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	watcher, err := m.LoadWatcher(id)
	if err != nil {
		return err
	}
	if watcher.ID == "" {
		return fmt.Errorf("watcher %q not found", id)
	}

	handle, err := m.NewProcessHandle(ProcessRecord{
		ID:           watcher.ID,
		Kind:         ProcessWatcher,
		Host:         watcher.Host,
		CWD:          watcher.CWD,
		Target:       watcher.Entity.DisplayName,
		State:        watcher.State,
		Provider:     watcher.Provider,
		Model:        watcher.Model,
		ApprovalMode: watcher.ApprovalMode,
	})
	if err != nil {
		return err
	}
	watcher.ProcessID = watcher.ID
	_ = m.SaveWatcher(watcher)
	defer func() {
		_ = handle.Finish(watcher.State)
	}()

	interval := durationOrDefault(watcher.IntervalSec, 30*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := m.evaluateWatcher(ctx, deps, &watcher, handle); err != nil {
		watcher.LastSummary = err.Error()
		watcher.State = "failed"
		_ = m.SaveWatcher(watcher)
		return err
	}

	for {
		select {
		case <-ctx.Done():
			watcher.State = "stopped"
			watcher.LastSummary = "watcher stopped"
			_ = m.SaveWatcher(watcher)
			return nil
		case <-ticker.C:
			if err := m.evaluateWatcher(ctx, deps, &watcher, handle); err != nil {
				watcher.State = "failed"
				watcher.LastSummary = err.Error()
				_ = m.SaveWatcher(watcher)
				return err
			}
		}
	}
}

func (m *Manager) RunShellRun(ctx context.Context, deps RuntimeDeps, id string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	status, err := m.LoadShellRunStatus(id)
	if err != nil {
		return err
	}
	if status.Spec.ID == "" {
		return fmt.Errorf("shell run %q not found", id)
	}

	handle, err := m.NewProcessHandle(ProcessRecord{
		ID:           status.Spec.ID,
		Kind:         ProcessShellRun,
		Host:         status.Spec.Host,
		CWD:          status.Spec.CWD,
		Target:       summarizeLine(status.Spec.Command),
		State:        "starting",
		Provider:     status.Spec.Provider,
		Model:        status.Spec.Model,
		ApprovalMode: status.Spec.ApprovalMode,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = handle.Finish(status.State.State)
	}()

	if err := os.MkdirAll(filepath.Dir(status.State.StdoutLogPath), 0o755); err != nil {
		return err
	}
	stdoutFile, err := os.OpenFile(status.State.StdoutLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer stdoutFile.Close()
	stderrFile, err := os.OpenFile(status.State.StderrLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer stderrFile.Close()

	maxRuntime := durationOrDefault(status.Spec.MaxRuntimeSec, time.Hour)
	runCtx, cancel := context.WithTimeout(ctx, maxRuntime)
	defer cancel()

	cmd := exec.CommandContext(runCtx, firstNonEmptyShell(status.Spec.Shell, "sh"), "-lc", status.Spec.Command)
	cmd.Dir = status.Spec.CWD
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		status.State.State = "failed"
		status.State.LastError = err.Error()
		status.State.FailureKind = core.ShellFailureStartup
		status.State.FinishedAt = time.Now().UTC()
		return m.SaveShellRunState(id, status.State)
	}
	status.State.PID = cmd.Process.Pid
	status.State.PGID = cmd.Process.Pid
	status.State.State = "running"
	status.State.LastSummary = "shell run started"
	_ = m.SaveShellRunState(id, status.State)

	type copyResult struct{ err error }
	copyDone := make(chan copyResult, 2)
	copyStream := func(dst *os.File, src io.Reader, stderr bool) {
		_, err := io.Copy(dst, src)
		copyDone <- copyResult{err: err}
	}
	go copyStream(stdoutFile, stdoutPipe, false)
	go copyStream(stderrFile, stderrPipe, true)

	stallWindow := durationOrDefault(status.Spec.StallWindowSec, 45*time.Second)
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	lastStdoutSize := int64(0)
	lastStderrSize := int64(0)
	status.State.LastOutputAt = time.Now().UTC()
	for {
		select {
		case err := <-waitDone:
			for i := 0; i < 2; i++ {
				<-copyDone
			}
			now := time.Now().UTC()
			status.State.FinishedAt = now
			status.State.LastOutputAt = now
			status.State.StdoutPreview = readFileTail(status.State.StdoutLogPath, deps.Runtime.Config.Tools.DurableShellLogPreviewBytes)
			status.State.StderrPreview = readFileTail(status.State.StderrLogPath, deps.Runtime.Config.Tools.DurableShellLogPreviewBytes)
			if err != nil {
				status.State.State = "failed"
				status.State.LastError = err.Error()
				status.State.LastSummary = firstNonEmptyShell(summarizeLine(status.State.StderrPreview), summarizeLine(status.State.StdoutPreview), "shell run failed")
				status.State.FailureKind = classifyShellWaitError(err, runCtx.Err(), false)
				if exitErr, ok := err.(*exec.ExitError); ok {
					status.State.ExitCode = exitErr.ExitCode()
					if waitStatus, ok := exitErr.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
						status.State.KilledBySignal = int(waitStatus.Signal())
					}
				}
			} else {
				status.State.State = "succeeded"
				status.State.LastSummary = firstNonEmptyShell(summarizeLine(status.State.StdoutPreview), "shell run succeeded")
			}
			_ = m.SaveShellRunState(id, status.State)
			_ = handle.Finish(status.State.State)
			return nil
		case <-heartbeat.C:
			stdoutSize := fileSize(status.State.StdoutLogPath)
			stderrSize := fileSize(status.State.StderrLogPath)
			if stdoutSize != lastStdoutSize || stderrSize != lastStderrSize {
				lastStdoutSize = stdoutSize
				lastStderrSize = stderrSize
				status.State.LastOutputAt = time.Now().UTC()
			}
			if !status.State.LastOutputAt.IsZero() && time.Since(status.State.LastOutputAt) > stallWindow {
				status.State.State = "stalled"
				status.State.LastSummary = fmt.Sprintf("no output for %s", stallWindow)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			status.State.StdoutPreview = readFileTail(status.State.StdoutLogPath, deps.Runtime.Config.Tools.DurableShellLogPreviewBytes)
			status.State.StderrPreview = readFileTail(status.State.StderrLogPath, deps.Runtime.Config.Tools.DurableShellLogPreviewBytes)
			_ = m.SaveShellRunState(id, status.State)
			_ = handle.Beat(status.State.State)
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			status.State.State = "stopped"
			status.State.LastSummary = "shell run stopped"
			status.State.FinishedAt = time.Now().UTC()
			_ = m.SaveShellRunState(id, status.State)
			_ = handle.Finish(status.State.State)
			return nil
		}
	}
}

func (m *Manager) evaluateWatcher(ctx context.Context, deps RuntimeDeps, watcher *Watcher, handle *ProcessHandle) error {
	report, evidenceText, err := m.probeAndCollect(ctx, deps, watcher.Entity, watcher.ID, "")
	if err != nil {
		return err
	}
	diagnosis := diagnose(report, evidenceText)
	now := time.Now().UTC()

	if report.Healthy {
		watcher.ConsecutiveFailures = 0
		watcher.LastHealthyAt = now
		if watcher.State == "recovering" || watcher.State == "incident" {
			if watcher.StableSince.IsZero() {
				watcher.StableSince = now
				watcher.LastSummary = "service recovered; holding in stability window"
			} else if now.Sub(watcher.StableSince) >= durationOrDefault(watcher.StabilityWindowSec, 5*time.Minute) {
				watcher.State = "cooldown"
				watcher.CooldownUntil = now.Add(durationOrDefault(watcher.CooldownSec, 15*time.Minute))
				watcher.LastSummary = "recovered and stable; entering cooldown"
				if watcher.CurrentIncidentID != "" {
					incident, _ := m.LoadIncident(watcher.CurrentIncidentID)
					if incident.ID != "" {
						incident.Status = "resolved"
						incident.ResolvedAt = now
						incident.Notes = append(incident.Notes, "watcher verified stability window")
						_ = m.SaveIncident(incident)
					}
					watcher.CurrentIncidentID = ""
				}
				watcher.Baseline = Baseline{
					CapturedAt:     now,
					Entity:         watcher.Entity,
					ExpectedPorts:  append([]int(nil), watcher.Entity.ExpectedPorts...),
					Manager:        watcher.Entity.Manager,
					HealthySignals: report.Statuses,
				}
			}
		} else if watcher.State == "cooldown" {
			if watcher.CooldownUntil.IsZero() || now.After(watcher.CooldownUntil) {
				watcher.State = "healthy"
				watcher.LastSummary = "watcher healthy"
			}
		} else {
			watcher.State = "healthy"
			watcher.LastSummary = "watcher healthy"
		}
		watcher.UpdatedAt = now
		_ = m.SaveWatcher(*watcher)
		return handle.Beat(watcher.State)
	}

	watcher.StableSince = time.Time{}
	watcher.ConsecutiveFailures++
	if watcher.ConsecutiveFailures < maxInt(watcher.FailureThreshold, 3) {
		watcher.State = "suspect"
		watcher.LastSummary = report.Summary
		watcher.UpdatedAt = now
		_ = m.SaveWatcher(*watcher)
		return handle.Beat(watcher.State)
	}

	if watcher.CurrentIncidentID == "" {
		watcher.IncidentCount++
		watcher.CurrentIncidentID = fmt.Sprintf("incident-%d", now.UnixNano())
	}
	incident := IncidentRecord{
		ID:          watcher.CurrentIncidentID,
		WatcherID:   watcher.ID,
		EntityID:    watcher.Entity.ID,
		Target:      watcher.Entity.DisplayName,
		Kind:        "watcher",
		Status:      "open",
		Summary:     diagnosis.Summary,
		Hypothesis:  diagnosis.Hypothesis,
		ObserveOnly: watcher.ObserveOnly,
		StartedAt:   now,
	}
	evidenceDir, err := m.captureEvidence(ctx, deps, watcher.Entity, incident.ID, "")
	if err == nil {
		incident.EvidenceDir = evidenceDir
	}

	if watcher.ObserveOnly || !diagnosis.RestartAllowed || watcher.RestartAttempts >= maxInt(watcher.RestartBudget, 1) {
		watcher.State = "incident"
		watcher.LastSummary = diagnosis.Summary
		incident.Notes = append(incident.Notes, diagnosis.Reasons...)
		_ = m.SaveIncident(incident)
		watcher.UpdatedAt = now
		_ = m.SaveWatcher(*watcher)
		return handle.Beat(watcher.State)
	}

	action, ok := remediationAction(watcher.Entity)
	if !ok {
		watcher.State = "incident"
		watcher.LastSummary = "no restart action is available for this target"
		incident.Notes = append(incident.Notes, "no restart action is available for this target")
		_ = m.SaveIncident(incident)
		watcher.UpdatedAt = now
		_ = m.SaveWatcher(*watcher)
		return handle.Beat(watcher.State)
	}
	result, blockedReason, err := executeDelegatedAction(ctx, deps, action)
	if blockedReason != "" {
		watcher.State = "incident"
		watcher.LastSummary = blockedReason
		incident.Notes = append(incident.Notes, blockedReason)
		_ = m.SaveIncident(incident)
		watcher.UpdatedAt = now
		_ = m.SaveWatcher(*watcher)
		return handle.Beat(watcher.State)
	}
	if err != nil {
		watcher.State = "incident"
		watcher.LastSummary = firstNonEmpty(result.Error, err.Error())
		incident.Commands = append(incident.Commands, action.Command)
		incident.Notes = append(incident.Notes, watcher.LastSummary)
		_ = m.SaveIncident(incident)
		watcher.UpdatedAt = now
		_ = m.SaveWatcher(*watcher)
		return handle.Beat(watcher.State)
	}

	watcher.RestartAttempts++
	watcher.State = "recovering"
	watcher.LastSummary = "restart issued; verifying recovery"
	incident.AutoRemediated = true
	incident.Commands = append(incident.Commands, action.Command)
	incident.Notes = append(incident.Notes, result.Summary)
	_ = m.SaveIncident(incident)
	watcher.UpdatedAt = now
	_ = m.SaveWatcher(*watcher)
	return handle.Beat(watcher.State)
}

func (m *Manager) RunRecovery(ctx context.Context, deps RuntimeDeps, id string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	spec, err := m.LoadRecoverySpec(id)
	if err != nil {
		return err
	}
	if spec.ID == "" {
		return fmt.Errorf("recovery %q not found", id)
	}
	state, err := m.LoadRecoveryState(id)
	if err != nil {
		return err
	}
	handle, err := m.NewProcessHandle(ProcessRecord{
		ID:           spec.ID,
		Kind:         ProcessRecovery,
		Host:         spec.Host,
		CWD:          spec.CWD,
		Target:       spec.Entity.DisplayName,
		State:        firstNonEmpty(state.Phase, "initialize"),
		Provider:     spec.Provider,
		Model:        spec.Model,
		ApprovalMode: spec.ApprovalMode,
	})
	if err != nil {
		return err
	}
	state.ProcessID = spec.ID
	_ = m.SaveRecoveryState(spec.ID, state)
	defer func() {
		_ = handle.Finish(state.Phase)
	}()

	step := func(phase, summary string, details ...string) error {
		state.Phase = phase
		state.LastSummary = summary
		state.UpdatedAt = time.Now().UTC()
		if err := m.SaveRecoveryState(spec.ID, state); err != nil {
			return err
		}
		if err := m.AppendRecoveryTimeline(spec.ID, TimelineEvent{
			At:      time.Now().UTC(),
			Phase:   phase,
			Type:    phase,
			Summary: summary,
			Details: details,
		}); err != nil {
			return err
		}
		progress := fmt.Sprintf("# Recovery %s\n\nGoal: %s\nTarget: %s\nPhase: %s\n\nLatest: %s\n", spec.ID, spec.Goal, spec.Entity.DisplayName, phase, summary)
		return m.WriteRecoveryProgress(spec.ID, progress)
	}

	if err := step("initialize", "resolved target and started recovery"); err != nil {
		return err
	}
	if err := handle.Beat("initialize"); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			state.Phase = "stopped"
			state.LastSummary = "recovery stopped"
			state.FinishedAt = time.Now().UTC()
			_ = m.SaveRecoveryState(spec.ID, state)
			return nil
		default:
		}

		report, evidenceText, err := m.probeAndCollect(ctx, deps, spec.Entity, "", spec.ID)
		if err != nil {
			state.Phase = "failed"
			state.LastError = err.Error()
			state.LastSummary = err.Error()
			state.FinishedAt = time.Now().UTC()
			_ = m.SaveRecoveryState(spec.ID, state)
			return err
		}
		if report.Healthy {
			if state.StableSince.IsZero() {
				state.Phase = "stabilize"
				state.StableSince = time.Now().UTC()
				if err := step("stabilize", "target healthy; waiting for stability window"); err != nil {
					return err
				}
				if err := handle.Beat("stabilize"); err != nil {
					return err
				}
				time.Sleep(10 * time.Second)
				continue
			}
			if time.Since(state.StableSince) >= durationOrDefault(spec.StabilityWindowSec, 5*time.Minute) {
				state.Phase = "done"
				state.LastSummary = "target recovered and remained stable"
				state.FinishedAt = time.Now().UTC()
				if err := m.SaveRecoveryState(spec.ID, state); err != nil {
					return err
				}
				if err := m.AppendRecoveryTimeline(spec.ID, TimelineEvent{
					At:      state.FinishedAt,
					Phase:   "done",
					Type:    "done",
					Summary: state.LastSummary,
				}); err != nil {
					return err
				}
				_ = m.WriteRecoveryProgress(spec.ID, fmt.Sprintf("# Recovery %s\n\nGoal: %s\nTarget: %s\n\nStatus: done\n\n%s\n", spec.ID, spec.Goal, spec.Entity.DisplayName, state.LastSummary))
				return nil
			}
			time.Sleep(10 * time.Second)
			continue
		}

		state.StableSince = time.Time{}
		diagnosis := diagnose(report, evidenceText)
		state.CurrentHypothesis = diagnosis.Hypothesis
		if err := step("diagnose", diagnosis.Summary, diagnosis.Reasons...); err != nil {
			return err
		}
		if err := handle.Beat("diagnose"); err != nil {
			return err
		}

		if state.Attempts >= maxInt(spec.RetryBudget, 3) {
			state.Phase = "failed"
			state.BlockedReason = "retry budget exhausted"
			state.LastSummary = "recovery stopped after exhausting retry budget"
			state.FinishedAt = time.Now().UTC()
			return m.SaveRecoveryState(spec.ID, state)
		}
		if !diagnosis.RestartAllowed {
			state.Phase = "blocked"
			state.BlockedReason = diagnosis.Summary
			state.LastSummary = diagnosis.Summary
			state.FinishedAt = time.Now().UTC()
			incidentID := fmt.Sprintf("incident-%d", time.Now().UTC().UnixNano())
			evidenceDir, _ := m.captureEvidence(ctx, deps, spec.Entity, incidentID, spec.ID)
			_ = m.SaveIncident(IncidentRecord{
				ID:         incidentID,
				RecoveryID: spec.ID,
				EntityID:   spec.Entity.ID,
				Target:     spec.Entity.DisplayName,
				Kind:       "recovery",
				Status:     "open",
				Summary:    diagnosis.Summary,
				Hypothesis: diagnosis.Hypothesis,
				EvidenceDir: evidenceDir,
				Notes:      diagnosis.Reasons,
				StartedAt:  time.Now().UTC(),
			})
			return m.SaveRecoveryState(spec.ID, state)
		}

		action, ok := remediationAction(spec.Entity)
		if !ok {
			state.Phase = "blocked"
			state.BlockedReason = "no restart action is available for this target"
			state.LastSummary = state.BlockedReason
			state.FinishedAt = time.Now().UTC()
			return m.SaveRecoveryState(spec.ID, state)
		}
		if err := step("remediate", "running restart action", action.Command); err != nil {
			return err
		}
		if err := handle.Beat("remediate"); err != nil {
			return err
		}
		result, blockedReason, err := executeDelegatedAction(ctx, deps, action)
		state.Attempts++
		if blockedReason != "" {
			state.Phase = "blocked"
			state.BlockedReason = blockedReason
			state.LastSummary = blockedReason
			state.FinishedAt = time.Now().UTC()
			return m.SaveRecoveryState(spec.ID, state)
		}
		if err != nil {
			state.LastError = firstNonEmpty(result.Error, err.Error())
			if state.Attempts >= maxInt(spec.RetryBudget, 3) {
				state.Phase = "failed"
				state.LastSummary = state.LastError
				state.FinishedAt = time.Now().UTC()
				return m.SaveRecoveryState(spec.ID, state)
			}
			time.Sleep(10 * time.Second)
			continue
		}
		if err := step("verify", "restart action succeeded; verifying health", result.Summary); err != nil {
			return err
		}
		if err := handle.Beat("verify"); err != nil {
			return err
		}
		time.Sleep(10 * time.Second)
	}
}

func (m *Manager) probeAndCollect(ctx context.Context, deps RuntimeDeps, entity LogicalEntity, watcherID, recoveryID string) (ProbeReport, string, error) {
	report, err := probeEntity(ctx, deps, entity)
	if err != nil {
		return ProbeReport{}, "", err
	}
	evidenceDir, err := m.captureEvidence(ctx, deps, entity, firstNonEmpty(watcherID, recoveryID, entity.ID), recoveryID)
	if err != nil {
		return report, "", nil
	}
	evidenceText, _ := readEvidenceText(evidenceDir)
	return report, evidenceText, nil
}

func (m *Manager) captureEvidence(ctx context.Context, deps RuntimeDeps, entity LogicalEntity, id, recoveryID string) (string, error) {
	root := filepath.Join(m.IncidentsDir, sanitizeID(id), "evidence")
	if recoveryID != "" {
		root = filepath.Join(m.RecoveryEvidenceDir(recoveryID), sanitizeID(id))
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	commands := evidenceCommands(entity)
	for name, command := range commands {
		output, _ := execCommand(ctx, deps, command)
		if writeErr := os.WriteFile(filepath.Join(root, name+".txt"), []byte(output), 0o600); writeErr != nil {
			return "", writeErr
		}
	}
	if deps.Graph != nil {
		events, _ := deps.Graph.Store.EventsSince(deps.Graph.HostKey(), time.Now().UTC().Add(-time.Hour))
		if len(events) > 0 {
			data, _ := json.MarshalIndent(events, "", "  ")
			_ = os.WriteFile(filepath.Join(root, "graph_events.json"), data, 0o600)
		}
	}
	return root, nil
}

func evidenceCommands(entity LogicalEntity) map[string]string {
	commands := map[string]string{
		"ps":   "ps aux | head -n 50",
		"ss":   "ss -ltnp | head -n 80",
		"disk": "df -h",
	}
	switch entity.Manager {
	case ManagerSystemd:
		commands["status"] = fmt.Sprintf("systemctl status %s --no-pager", entity.Unit)
		commands["journal"] = fmt.Sprintf("journalctl -u %s -n 120 --no-pager", entity.Unit)
	case ManagerDocker:
		commands["inspect"] = fmt.Sprintf("docker inspect %s", entity.Container)
		commands["logs"] = fmt.Sprintf("docker logs --tail 120 %s", entity.Container)
	default:
		if entity.Port > 0 {
			commands["listener"] = fmt.Sprintf("ss -ltnp | grep -E ':%d\\b' || true", entity.Port)
		}
	}
	if entity.HealthURL != "" {
		commands["health"] = fmt.Sprintf("command -v curl >/dev/null 2>&1 && curl -fsS -i %s || true", shellEscape(entity.HealthURL))
	}
	return commands
}

func probeEntity(ctx context.Context, deps RuntimeDeps, entity LogicalEntity) (ProbeReport, error) {
	statuses := []ProbeStatus{}
	report := ProbeReport{Entity: entity}
	switch entity.Manager {
	case ManagerSystemd:
		output, _ := execCommand(ctx, deps, fmt.Sprintf("systemctl is-active %s || true", shellEscape(entity.Unit)))
		healthy := strings.TrimSpace(output) == "active"
		statuses = append(statuses, ProbeStatus{Type: "systemd_active", Label: entity.Unit, Healthy: healthy, Detail: strings.TrimSpace(output)})
		report.SelfDown = !healthy
	case ManagerDocker:
		output, _ := execCommand(ctx, deps, fmt.Sprintf("docker inspect -f '{{.State.Running}}' %s 2>/dev/null || true", shellEscape(entity.Container)))
		healthy := strings.TrimSpace(output) == "true"
		statuses = append(statuses, ProbeStatus{Type: "docker_running", Label: entity.Container, Healthy: healthy, Detail: strings.TrimSpace(output)})
		report.SelfDown = !healthy
	default:
		if entity.Port > 0 {
			output, _ := execCommand(ctx, deps, fmt.Sprintf("ss -ltnp | grep -E ':%d\\b' || true", entity.Port))
			healthy := strings.TrimSpace(output) != ""
			statuses = append(statuses, ProbeStatus{Type: "listener", Label: strconv.Itoa(entity.Port), Healthy: healthy, Detail: summarizeLine(output)})
			report.SelfDown = !healthy
		}
	}
	for _, port := range entity.ExpectedPorts {
		if port <= 0 {
			continue
		}
		output, _ := execCommand(ctx, deps, fmt.Sprintf("ss -ltnp | grep -E ':%d\\b' || true", port))
		healthy := strings.TrimSpace(output) != ""
		statuses = append(statuses, ProbeStatus{Type: "port_listening", Label: strconv.Itoa(port), Healthy: healthy, Detail: summarizeLine(output)})
		if !healthy {
			report.SelfDown = true
		}
	}
	if entity.HealthURL != "" {
		output, _ := execCommand(ctx, deps, fmt.Sprintf("command -v curl >/dev/null 2>&1 && curl -fsS -o /dev/null -w '%%{http_code}' %s || true", shellEscape(entity.HealthURL)))
		code := strings.TrimSpace(output)
		healthy := code == "200" || code == "204"
		if code != "" && code != "0" {
			statuses = append(statuses, ProbeStatus{Type: "http", Label: entity.HealthURL, Healthy: healthy, Detail: code})
			if !healthy {
				report.SelfDown = true
			}
		}
	}
	report.Statuses = statuses
	report.Healthy = len(statuses) > 0
	for _, status := range statuses {
		if !status.Healthy {
			report.Healthy = false
			break
		}
	}
	if report.Healthy {
		report.Summary = "all active probes are healthy"
	} else if report.SelfDown {
		report.Summary = "target failed one or more active probes"
	} else {
		report.Summary = "health is unknown"
	}
	return report, nil
}

func diagnose(report ProbeReport, evidence string) Diagnosis {
	lower := strings.ToLower(evidence)
	report.DiskPressure = strings.Contains(lower, "100%") || strings.Contains(lower, " 99%")
	report.OOMDetected = strings.Contains(lower, "oom") || strings.Contains(lower, "killed process")
	report.PortConflict = strings.Contains(lower, "address already in use") || strings.Contains(lower, "bind: ") || strings.Contains(lower, "port is already allocated")
	report.ConfigError = strings.Contains(lower, "syntax error") || strings.Contains(lower, "invalid") || strings.Contains(lower, "unknown directive") || strings.Contains(lower, "failed to load config")
	report.DependencyDown = strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route to host") || strings.Contains(lower, "dial tcp") || strings.Contains(lower, "name resolution")

	diagnosis := Diagnosis{
		Hypothesis:      "unknown",
		Summary:         "incident needs manual investigation",
		RestartAllowed:  false,
		ResourceBlocker: report.DiskPressure || report.OOMDetected || report.PortConflict,
		ConfigBlocker:   report.ConfigError,
		DependencyBlocker: report.DependencyDown,
	}
	switch {
	case report.DiskPressure || report.OOMDetected || report.PortConflict:
		diagnosis.Hypothesis = "resource_exhaustion"
		diagnosis.Summary = "resource pressure or port conflict is blocking startup"
		diagnosis.Reasons = append(diagnosis.Reasons, "resource blocker detected in evidence")
	case report.ConfigError:
		diagnosis.Hypothesis = "config_start_failure"
		diagnosis.Summary = "service has a config or startup error; restart withheld"
		diagnosis.Reasons = append(diagnosis.Reasons, "config or startup error detected in evidence")
	case report.DependencyDown:
		diagnosis.Hypothesis = "dependency_down"
		diagnosis.Summary = "dependency failure is likely; restart withheld"
		diagnosis.Reasons = append(diagnosis.Reasons, "dependency-related failures detected in evidence")
	case report.SelfDown:
		diagnosis.Hypothesis = "target_down"
		diagnosis.Summary = "target is down and no higher-confidence blocker was found"
		diagnosis.Reasons = append(diagnosis.Reasons, "active probes show the target is down")
		diagnosis.RestartAllowed = true
	default:
		diagnosis.Reasons = append(diagnosis.Reasons, "active probes were inconclusive")
	}
	return diagnosis
}

func remediationAction(entity LogicalEntity) (core.Action, bool) {
	switch entity.Manager {
	case ManagerSystemd:
		return core.Action{
			ID:      fmt.Sprintf("restart-%d", time.Now().UTC().UnixNano()),
			Type:    core.ActionRunShell,
			Title:   "restart " + entity.Unit,
			Reason:  "service is down and restart-only remediation is allowed",
			Command: "systemctl restart " + shellEscape(entity.Unit),
		}, true
	case ManagerDocker:
		return core.Action{
			ID:      fmt.Sprintf("restart-%d", time.Now().UTC().UnixNano()),
			Type:    core.ActionRunShell,
			Title:   "restart " + entity.Container,
			Reason:  "container is down and restart-only remediation is allowed",
			Command: "docker restart " + shellEscape(entity.Container),
		}, true
	default:
		return core.Action{}, false
	}
}

func executeDelegatedAction(ctx context.Context, deps RuntimeDeps, action core.Action) (core.ActionResult, string, error) {
	resolution, err := deps.Policy.Resolve(action)
	if err != nil {
		return core.ActionResult{}, "", err
	}
	if resolution.Match.Matched && resolution.Match.Decision == policy.DecisionDeny {
		return core.ActionResult{}, "blocked by machine policy", nil
	}
	report, err := deps.Executor.Preview(action, false)
	if err != nil {
		return core.ActionResult{}, "", err
	}
	if report.Level == core.RiskHigh || deps.Executor.Classifier.IsSensitiveAction(action) {
		return core.ActionResult{}, "delegated runtime will not run high-risk or sensitive actions", nil
	}
	result, err := deps.Executor.Execute(ctx, action, false)
	return result, "", err
}

func execCommand(ctx context.Context, deps RuntimeDeps, command string) (string, error) {
	action := core.Action{
		ID:         fmt.Sprintf("probe-%d", time.Now().UTC().UnixNano()),
		Type:       core.ActionRunShell,
		Title:      command,
		Command:    command,
		Reason:     "ops evidence capture",
		TimeoutSec: 20,
	}
	result, err := deps.Executor.Execute(ctx, action, false)
	return strings.TrimSpace(firstNonEmpty(result.Stdout, result.Stderr)), err
}

func readEvidenceText(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		buf.WriteString("\n== " + entry.Name() + " ==\n")
		buf.Write(data)
	}
	return buf.String(), nil
}

func launchDetached(executable string, args []string, cwd string) (int, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer devNull.Close()

	cmd := exec.Command(executable, args...)
	cmd.Dir = cwd
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func durationOrDefault(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func hasStrongProbe(entity LogicalEntity) bool {
	return entity.Manager == ManagerSystemd || entity.Manager == ManagerDocker || entity.Port > 0 || entity.HealthURL != ""
}

func summarizeLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func shellEscape(value string) string {
	if strings.TrimSpace(value) == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func firstNonEmptyShell(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func readFileTail(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
	}
	return string(data)
}

func classifyShellWaitError(err error, ctxErr error, stalled bool) core.ShellFailureKind {
	if stalled {
		return core.ShellFailureStalled
	}
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return core.ShellFailureTimeout
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return core.ShellFailureSignalKilled
		}
		return core.ShellFailureNonZeroExit
	}
	return core.ShellFailureUnknown
}

func StopByIDOrPID(records []ProcessRecord, idOrPID string, sig syscall.Signal) (ProcessRecord, error) {
	idOrPID = strings.TrimSpace(idOrPID)
	if idOrPID == "" {
		return ProcessRecord{}, fmt.Errorf("process id is required")
	}
	for _, record := range records {
		if record.ID == idOrPID {
			if record.PID <= 0 {
				return ProcessRecord{}, fmt.Errorf("process %s does not have a live pid", idOrPID)
			}
			return record, syscall.Kill(record.PID, sig)
		}
	}
	if pid, err := strconv.Atoi(idOrPID); err == nil {
		for _, record := range records {
			if record.PID == pid {
				return record, syscall.Kill(record.PID, sig)
			}
		}
		return ProcessRecord{PID: pid}, syscall.Kill(pid, sig)
	}
	return ProcessRecord{}, fmt.Errorf("process %s was not found", idOrPID)
}
