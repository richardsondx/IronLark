package ops

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	"github.com/richardsondx/IronLark/internal/core"
)

type Manager struct {
	OpsDir       string
	WatchersDir  string
	RunsDir      string
	ShellRunsDir string
	IncidentsDir string
	ProcessesDir string
}

func NewManager(paths cfgpkg.Paths) *Manager {
	return &Manager{
		OpsDir:       paths.OpsDir,
		WatchersDir:  paths.WatchersDir,
		RunsDir:      paths.RunsDir,
		ShellRunsDir: paths.ShellRunsDir,
		IncidentsDir: paths.IncidentsDir,
		ProcessesDir: paths.ProcessesDir,
	}
}

func (m *Manager) watcherDir(id string) string {
	return filepath.Join(m.WatchersDir, sanitizeID(id))
}

func (m *Manager) runDir(id string) string {
	return filepath.Join(m.RunsDir, sanitizeID(id))
}

func (m *Manager) shellRunDir(id string) string {
	return filepath.Join(m.ShellRunsDir, sanitizeID(id))
}

func (m *Manager) incidentPath(id string) string {
	return filepath.Join(m.IncidentsDir, sanitizeID(id)+".json")
}

func (m *Manager) processPath(id string) string {
	return filepath.Join(m.ProcessesDir, sanitizeID(id)+".json")
}

func (m *Manager) SaveWatcher(w Watcher) error {
	if strings.TrimSpace(w.ID) == "" {
		return fmt.Errorf("watcher id is required")
	}
	if err := os.MkdirAll(m.watcherDir(w.ID), 0o755); err != nil {
		return err
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now().UTC()
	}
	w.UpdatedAt = time.Now().UTC()
	return writeJSON(filepath.Join(m.watcherDir(w.ID), "watcher.json"), w)
}

func (m *Manager) LoadWatcher(id string) (Watcher, error) {
	var watcher Watcher
	err := readJSON(filepath.Join(m.watcherDir(id), "watcher.json"), &watcher)
	if os.IsNotExist(err) {
		return Watcher{}, nil
	}
	return watcher, err
}

func (m *Manager) ListWatchers() ([]Watcher, error) {
	entries, err := os.ReadDir(m.WatchersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Watcher, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := m.LoadWatcher(entry.Name())
		if err == nil && record.ID != "" {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *Manager) SaveRecoverySpec(spec RecoverySpec) error {
	if strings.TrimSpace(spec.ID) == "" {
		return fmt.Errorf("recovery id is required")
	}
	if err := os.MkdirAll(m.runDir(spec.ID), 0o755); err != nil {
		return err
	}
	if spec.CreatedAt.IsZero() {
		spec.CreatedAt = time.Now().UTC()
	}
	return writeJSON(filepath.Join(m.runDir(spec.ID), "spec.json"), spec)
}

func (m *Manager) LoadRecoverySpec(id string) (RecoverySpec, error) {
	var spec RecoverySpec
	err := readJSON(filepath.Join(m.runDir(id), "spec.json"), &spec)
	if os.IsNotExist(err) {
		return RecoverySpec{}, nil
	}
	return spec, err
}

func (m *Manager) SaveRecoveryState(id string, state RecoveryState) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("recovery id is required")
	}
	if err := os.MkdirAll(m.runDir(id), 0o755); err != nil {
		return err
	}
	if state.StartedAt.IsZero() {
		state.StartedAt = time.Now().UTC()
	}
	state.UpdatedAt = time.Now().UTC()
	return writeJSON(filepath.Join(m.runDir(id), "state.json"), state)
}

func (m *Manager) LoadRecoveryState(id string) (RecoveryState, error) {
	var state RecoveryState
	err := readJSON(filepath.Join(m.runDir(id), "state.json"), &state)
	if os.IsNotExist(err) {
		return RecoveryState{}, nil
	}
	return state, err
}

func (m *Manager) AppendRecoveryTimeline(id string, event TimelineEvent) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("recovery id is required")
	}
	if err := os.MkdirAll(m.runDir(id), 0o755); err != nil {
		return err
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	f, err := os.OpenFile(filepath.Join(m.runDir(id), "timeline.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(data))
	return err
}

func (m *Manager) LoadRecoveryTimeline(id string) ([]TimelineEvent, error) {
	path := filepath.Join(m.runDir(id), "timeline.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	events := []TimelineEvent{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event TimelineEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err == nil {
			events = append(events, event)
		}
	}
	return events, scanner.Err()
}

func (m *Manager) WriteRecoveryProgress(id, content string) error {
	if err := os.MkdirAll(m.runDir(id), 0o755); err != nil {
		return err
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(filepath.Join(m.runDir(id), "progress.md"), []byte(content), 0o600)
}

func (m *Manager) ReadRecoveryProgress(id string) (string, error) {
	data, err := os.ReadFile(filepath.Join(m.runDir(id), "progress.md"))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

func (m *Manager) RecoveryEvidenceDir(id string) string {
	return filepath.Join(m.runDir(id), "evidence")
}

func (m *Manager) SaveIncident(record IncidentRecord) error {
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("incident id is required")
	}
	if err := os.MkdirAll(m.IncidentsDir, 0o755); err != nil {
		return err
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	record.UpdatedAt = time.Now().UTC()
	return writeJSON(m.incidentPath(record.ID), record)
}

func (m *Manager) LoadIncident(id string) (IncidentRecord, error) {
	var record IncidentRecord
	err := readJSON(m.incidentPath(id), &record)
	if os.IsNotExist(err) {
		return IncidentRecord{}, nil
	}
	return record, err
}

func (m *Manager) ListIncidents() ([]IncidentRecord, error) {
	entries, err := os.ReadDir(m.IncidentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]IncidentRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := m.LoadIncident(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil && record.ID != "" {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *Manager) SaveProcess(record ProcessRecord) error {
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("process id is required")
	}
	if err := os.MkdirAll(m.ProcessesDir, 0o755); err != nil {
		return err
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	if record.LastHeartbeatAt.IsZero() {
		record.LastHeartbeatAt = time.Now().UTC()
	}
	return writeJSON(m.processPath(record.ID), record)
}

func (m *Manager) LoadProcess(id string) (ProcessRecord, error) {
	var record ProcessRecord
	err := readJSON(m.processPath(id), &record)
	if os.IsNotExist(err) {
		return ProcessRecord{}, nil
	}
	return record, err
}

func (m *Manager) ListProcesses() ([]ProcessRecord, error) {
	entries, err := os.ReadDir(m.ProcessesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ProcessRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := m.LoadProcess(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil && record.ID != "" {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastHeartbeatAt.After(out[j].LastHeartbeatAt)
	})
	return out, nil
}

func (m *Manager) DeleteProcess(id string) error {
	err := os.Remove(m.processPath(id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *Manager) SaveShellRunSpec(spec ShellRunSpec) error {
	if strings.TrimSpace(spec.ID) == "" {
		return fmt.Errorf("shell run id is required")
	}
	if err := os.MkdirAll(m.shellRunDir(spec.ID), 0o755); err != nil {
		return err
	}
	if spec.CreatedAt.IsZero() {
		spec.CreatedAt = time.Now().UTC()
	}
	return writeJSON(filepath.Join(m.shellRunDir(spec.ID), "spec.json"), spec)
}

func (m *Manager) LoadShellRunSpec(id string) (ShellRunSpec, error) {
	var spec ShellRunSpec
	err := readJSON(filepath.Join(m.shellRunDir(id), "spec.json"), &spec)
	if os.IsNotExist(err) {
		return ShellRunSpec{}, nil
	}
	return spec, err
}

func (m *Manager) SaveShellRunState(id string, state ShellRunState) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("shell run id is required")
	}
	if err := os.MkdirAll(m.shellRunDir(id), 0o755); err != nil {
		return err
	}
	if state.StartedAt.IsZero() {
		state.StartedAt = time.Now().UTC()
	}
	state.UpdatedAt = time.Now().UTC()
	return writeJSON(filepath.Join(m.shellRunDir(id), "state.json"), state)
}

func (m *Manager) LoadShellRunState(id string) (ShellRunState, error) {
	var state ShellRunState
	err := readJSON(filepath.Join(m.shellRunDir(id), "state.json"), &state)
	if os.IsNotExist(err) {
		return ShellRunState{}, nil
	}
	return state, err
}

func (m *Manager) LoadShellRunStatus(id string) (ShellRunStatus, error) {
	spec, err := m.LoadShellRunSpec(id)
	if err != nil {
		return ShellRunStatus{}, err
	}
	state, err := m.LoadShellRunState(id)
	if err != nil {
		return ShellRunStatus{}, err
	}
	return ShellRunStatus{Spec: spec, State: state}, nil
}

func (m *Manager) ListShellRuns() ([]ShellRunStatus, error) {
	entries, err := os.ReadDir(m.ShellRunsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ShellRunStatus, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := m.LoadShellRunStatus(entry.Name())
		if err == nil && record.Spec.ID != "" {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].State.UpdatedAt.After(out[j].State.UpdatedAt)
	})
	return out, nil
}

func (m *Manager) ShellRunStdoutPath(id string) string {
	return filepath.Join(m.shellRunDir(id), "stdout.log")
}

func (m *Manager) ShellRunStderrPath(id string) string {
	return filepath.Join(m.shellRunDir(id), "stderr.log")
}

func (m *Manager) LoadRecoveryStatus(id string) (RecoveryStatus, error) {
	spec, err := m.LoadRecoverySpec(id)
	if err != nil {
		return RecoveryStatus{}, err
	}
	state, err := m.LoadRecoveryState(id)
	if err != nil {
		return RecoveryStatus{}, err
	}
	return RecoveryStatus{Spec: spec, State: state}, nil
}

func (m *Manager) ListRecoveries() ([]RecoveryStatus, error) {
	entries, err := os.ReadDir(m.RunsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]RecoveryStatus, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := m.LoadRecoveryStatus(entry.Name())
		if err == nil && record.Spec.ID != "" {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].State.UpdatedAt.After(out[j].State.UpdatedAt)
	})
	return out, nil
}

func (m *Manager) Summary() (Summary, error) {
	processes, err := m.ListProcesses()
	if err != nil {
		return Summary{}, err
	}
	incidents, err := m.ListIncidents()
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, process := range processes {
		if !processAlive(process.PID) || process.State == "stopped" || process.State == "done" || process.State == "failed" {
			continue
		}
		switch process.Kind {
		case ProcessWatcher:
			summary.ActiveWatchers++
		case ProcessRecovery:
			summary.ActiveRecoveries++
		case ProcessShellRun:
			summary.ActiveShellRuns++
		}
	}
	for _, incident := range incidents {
		if incident.Status != "resolved" {
			summary.OpenIncidents++
		}
	}
	return summary, nil
}

func (m *Manager) SummaryLine() string {
	summary, err := m.Summary()
	if err != nil {
		return ""
	}
	parts := []string{}
	if summary.ActiveWatchers > 0 {
		parts = append(parts, fmt.Sprintf("%d watcher", summary.ActiveWatchers))
	}
	if summary.ActiveRecoveries > 0 {
		parts = append(parts, fmt.Sprintf("%d recovery", summary.ActiveRecoveries))
	}
	if summary.ActiveShellRuns > 0 {
		parts = append(parts, fmt.Sprintf("%d run", summary.ActiveShellRuns))
	}
	if summary.OpenIncidents > 0 {
		parts = append(parts, fmt.Sprintf("%d incident", summary.OpenIncidents))
	}
	if len(parts) == 0 {
		return ""
	}
	return "ops: " + strings.Join(parts, "  ")
}

func (m *Manager) Fetch(query string, since time.Time, limit int) (string, error) {
	result, err := m.Query(query, since, limit)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (m *Manager) Query(query string, since time.Time, limit int) (QueryResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	match := func(values ...string) bool {
		if query == "" {
			return true
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), query) {
				return true
			}
		}
		return false
	}
	result := QueryResult{}
	watchers, err := m.ListWatchers()
	if err != nil {
		return QueryResult{}, err
	}
	for _, watcher := range watchers {
		if match(watcher.ID, watcher.Query, watcher.Entity.DisplayName, watcher.Entity.Name, watcher.LastSummary) {
			result.Watchers = append(result.Watchers, watcher)
			if limit > 0 && len(result.Watchers) >= limit {
				break
			}
		}
	}
	recoveries, err := m.ListRecoveries()
	if err != nil {
		return QueryResult{}, err
	}
	for _, recovery := range recoveries {
		if !since.IsZero() && recovery.State.UpdatedAt.Before(since) {
			continue
		}
		if match(recovery.Spec.ID, recovery.Spec.Goal, recovery.Spec.Query, recovery.Spec.Entity.DisplayName, recovery.State.LastSummary) {
			result.Recoveries = append(result.Recoveries, recovery)
			if limit > 0 && len(result.Recoveries) >= limit {
				break
			}
		}
	}
	shellRuns, err := m.ListShellRuns()
	if err != nil {
		return QueryResult{}, err
	}
	for _, shellRun := range shellRuns {
		if !since.IsZero() && shellRun.State.UpdatedAt.Before(since) {
			continue
		}
		if match(shellRun.Spec.ID, shellRun.Spec.Command, shellRun.Spec.CWD, shellRun.State.LastSummary, shellRun.State.LastError) {
			result.ShellRuns = append(result.ShellRuns, shellRun)
			if limit > 0 && len(result.ShellRuns) >= limit {
				break
			}
		}
	}
	incidents, err := m.ListIncidents()
	if err != nil {
		return QueryResult{}, err
	}
	for _, incident := range incidents {
		if !since.IsZero() && incident.UpdatedAt.Before(since) {
			continue
		}
		if match(incident.ID, incident.Target, incident.Summary, incident.Hypothesis, strings.Join(incident.Notes, " ")) {
			result.Incidents = append(result.Incidents, incident)
			if limit > 0 && len(result.Incidents) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *Manager) NewProcessHandle(record ProcessRecord) (*ProcessHandle, error) {
	record.PID = os.Getpid()
	record.LastHeartbeatAt = time.Now().UTC()
	if err := m.SaveProcess(record); err != nil {
		return nil, err
	}
	return &ProcessHandle{manager: m, record: record}, nil
}

type ProcessHandle struct {
	manager *Manager
	record  ProcessRecord
}

func (h *ProcessHandle) Record() ProcessRecord {
	return h.record
}

func (h *ProcessHandle) Beat(state string) error {
	if state != "" {
		h.record.State = state
	}
	h.record.PID = os.Getpid()
	h.record.LastHeartbeatAt = time.Now().UTC()
	return h.manager.SaveProcess(h.record)
}

func (h *ProcessHandle) AddUsage(usage core.TokenUsage, requestDelta int) error {
	h.record.TokenUsage = h.record.TokenUsage.Add(usage)
	h.record.RequestCount += requestDelta
	return h.Beat(h.record.State)
}

func (h *ProcessHandle) Finish(state string) error {
	h.record.State = state
	h.record.FinishedAt = time.Now().UTC()
	h.record.LastHeartbeatAt = h.record.FinishedAt
	return h.manager.SaveProcess(h.record)
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, " ", "_")
	if value == "" {
		return strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	}
	return value
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
