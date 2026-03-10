package threads

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/state"
)

type ScopeMode string

const (
	ScopeAutoShell ScopeMode = "auto-shell"
	ScopeCWD       ScopeMode = "cwd"
	ScopeManual    ScopeMode = "manual"
)

type ThreadRef struct {
	ThreadID    string    `json:"thread_id"`
	ScopeKey    string    `json:"scope_key"`
	Scope       ScopeMode `json:"scope"`
	Source      string    `json:"source"`
	Manual      bool      `json:"manual"`
	Degraded    bool      `json:"degraded"`
	ParentPID   int       `json:"parent_pid,omitempty"`
	ParentStart string    `json:"parent_start,omitempty"`
	WorkingDir  string    `json:"working_dir"`
	Host        string    `json:"host"`
	User        string    `json:"user"`
}

type ThreadTurn struct {
	UserPrompt       string                `json:"user_prompt"`
	AssistantSummary string                `json:"assistant_summary,omitempty"`
	Findings         []string              `json:"findings,omitempty"`
	ActionSummary    string                `json:"action_summary,omitempty"`
	ResultSummary    string                `json:"result_summary,omitempty"`
	CompletionStatus core.CompletionStatus `json:"completion_status,omitempty"`
	EstimatedTokens  int                   `json:"estimated_tokens"`
	CreatedAt        time.Time             `json:"created_at"`
}

type Thread struct {
	ID              string       `json:"id"`
	ScopeKey        string       `json:"scope_key"`
	Scope           ScopeMode    `json:"scope"`
	CWD             string       `json:"cwd"`
	Host            string       `json:"host"`
	User            string       `json:"user"`
	ParentPID       int          `json:"parent_pid,omitempty"`
	ParentStart     string       `json:"parent_start,omitempty"`
	RollingSummary  string       `json:"rolling_summary,omitempty"`
	Turns           []ThreadTurn `json:"turns,omitempty"`
	EstimatedTokens int          `json:"estimated_tokens"`
	EstimatedChars  int          `json:"estimated_chars"`
	LastWarning     bool         `json:"last_warning"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

type ContextStats struct {
	ThreadID            string    `json:"thread_id"`
	Scope               ScopeMode `json:"scope"`
	Source              string    `json:"source"`
	Warning             bool      `json:"warning"`
	EstimatedTokens     int       `json:"estimated_tokens"`
	MaxTokens           int       `json:"max_tokens"`
	WarnThreshold       int       `json:"warn_threshold"`
	TurnCount           int       `json:"turn_count"`
	RollingSummary      string    `json:"rolling_summary,omitempty"`
	RecentTurns         []string  `json:"recent_turns,omitempty"`
	CWD                 string    `json:"cwd"`
	LastUpdated         time.Time `json:"last_updated"`
	PreviewMessageCount int       `json:"preview_message_count"`
}

type OverrideRecord struct {
	ThreadID  string    `json:"thread_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct {
	Dir string
}

type PromptOptions struct {
	RecentTurns int
}

type AppendOptions struct {
	ResultCharLimit int
	ThreadConfig    cfgpkg.ThreadConfig
}

var lookupParentStart = parentStartTime

func ResolveDefaultThread(runtime state.Runtime) (ThreadRef, error) {
	currentUser := "unknown"
	if u, err := user.Current(); err == nil {
		if u.Username != "" {
			currentUser = u.Username
		}
	}
	host := "unknown"
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		host = strings.TrimSpace(h)
	}

	if strings.TrimSpace(runtime.ThreadID) != "" {
		return ThreadRef{
			ThreadID:   strings.TrimSpace(runtime.ThreadID),
			ScopeKey:   "manual:" + strings.TrimSpace(runtime.ThreadID),
			Scope:      ScopeManual,
			Source:     "flag",
			Manual:     true,
			WorkingDir: runtime.WorkingDir,
			Host:       host,
			User:       currentUser,
		}, nil
	}

	if threadID, ok, err := loadOverride(runtime.Paths.ThreadsDir, currentUser, host, runtime.WorkingDir); err == nil && ok {
		return ThreadRef{
			ThreadID:   threadID,
			ScopeKey:   "manual:" + threadID,
			Scope:      ScopeManual,
			Source:     "override",
			Manual:     true,
			WorkingDir: runtime.WorkingDir,
			Host:       host,
			User:       currentUser,
		}, nil
	} else if err != nil {
		return ThreadRef{}, err
	}

	scope := ScopeMode(runtime.Config.Thread.Scope)
	if scope == "" {
		scope = ScopeAutoShell
	}

	parentPID := os.Getppid()
	parentStart, _ := lookupParentStart(parentPID)
	degraded := false
	scopeKey := fmt.Sprintf("%s|%s|%s", currentUser, host, runtime.WorkingDir)
	source := "cwd"
	if scope == ScopeAutoShell {
		if parentPID > 1 && parentStart != "" {
			scopeKey = fmt.Sprintf("%s|%s|%s|%d|%s", currentUser, host, runtime.WorkingDir, parentPID, parentStart)
			source = "auto-shell"
		} else {
			degraded = true
			source = "cwd-fallback"
		}
	}
	if scope == ScopeCWD {
		source = "cwd"
	}

	threadID := shortHash(scopeKey)
	if runtime.NewThread {
		now := time.Now().UTC().Format("20060102T150405")
		threadID = shortHash(scopeKey + "|" + now)
		source = source + "-new"
	}

	return ThreadRef{
		ThreadID:    threadID,
		ScopeKey:    scopeKey,
		Scope:       scope,
		Source:      source,
		Degraded:    degraded,
		ParentPID:   parentPID,
		ParentStart: parentStart,
		WorkingDir:  runtime.WorkingDir,
		Host:        host,
		User:        currentUser,
	}, nil
}

func (s Store) Load(threadID string) (Thread, error) {
	if strings.TrimSpace(threadID) == "" {
		return Thread{}, fmt.Errorf("thread id is required")
	}
	data, err := os.ReadFile(s.threadPath(threadID))
	if err != nil {
		if os.IsNotExist(err) {
			return Thread{ID: threadID}, nil
		}
		return Thread{}, fmt.Errorf("read thread: %w", err)
	}
	var thread Thread
	if err := json.Unmarshal(data, &thread); err != nil {
		return Thread{}, fmt.Errorf("unmarshal thread: %w", err)
	}
	if thread.ID == "" {
		thread.ID = threadID
	}
	return thread, nil
}

func (s Store) Save(thread Thread) error {
	if strings.TrimSpace(thread.ID) == "" {
		return fmt.Errorf("thread id is required")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create threads directory: %w", err)
	}
	if thread.CreatedAt.IsZero() {
		thread.CreatedAt = time.Now().UTC()
	}
	thread.UpdatedAt = time.Now().UTC()
	thread.EstimatedChars = estimateThreadChars(thread)
	thread.EstimatedTokens = estimateTokens(thread.EstimatedChars)
	data, err := json.MarshalIndent(thread, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal thread: %w", err)
	}
	if err := os.WriteFile(s.threadPath(thread.ID), data, 0o600); err != nil {
		return fmt.Errorf("write thread: %w", err)
	}
	return nil
}

func (s Store) Clear(threadID string) error {
	thread, err := s.Load(threadID)
	if err != nil {
		return err
	}
	thread.RollingSummary = ""
	thread.Turns = nil
	thread.EstimatedChars = 0
	thread.EstimatedTokens = 0
	thread.LastWarning = false
	return s.Save(thread)
}

func (s Store) Delete(threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return fmt.Errorf("thread id is required")
	}
	err := os.Remove(s.threadPath(threadID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete thread: %w", err)
	}
	return nil
}

func (s Store) List() ([]Thread, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list threads: %w", err)
	}
	threads := []Thread{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "overrides.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			continue
		}
		var thread Thread
		if err := json.Unmarshal(data, &thread); err == nil {
			threads = append(threads, thread)
		}
	}
	sort.Slice(threads, func(i, j int) bool {
		return threads[i].UpdatedAt.After(threads[j].UpdatedAt)
	})
	return threads, nil
}

func (s Store) Stats(threadID string, cfg cfgpkg.ThreadConfig, source string) (ContextStats, error) {
	thread, err := s.Load(threadID)
	if err != nil {
		return ContextStats{}, err
	}
	recent := []string{}
	for _, turn := range trimTurns(thread.Turns, cfg.RecentTurns) {
		recent = append(recent, summarizeRecentTurn(turn))
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 12000
	}
	warnThreshold := int(float64(maxTokens) * warnRatio(cfg))
	return ContextStats{
		ThreadID:            thread.ID,
		Scope:               thread.Scope,
		Source:              source,
		Warning:             thread.EstimatedTokens >= warnThreshold,
		EstimatedTokens:     thread.EstimatedTokens,
		MaxTokens:           maxTokens,
		WarnThreshold:       warnThreshold,
		TurnCount:           len(thread.Turns),
		RollingSummary:      thread.RollingSummary,
		RecentTurns:         recent,
		CWD:                 thread.CWD,
		LastUpdated:         thread.UpdatedAt,
		PreviewMessageCount: previewMessageCount(thread, cfg),
	}, nil
}

func (s Store) UpsertOverride(ref ThreadRef) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create threads directory: %w", err)
	}
	overrides, err := s.readOverrides()
	if err != nil {
		return err
	}
	overrides[overrideKey(ref.User, ref.Host, ref.WorkingDir)] = OverrideRecord{
		ThreadID:  ref.ThreadID,
		UpdatedAt: time.Now().UTC(),
	}
	return s.writeOverrides(overrides)
}

func (s Store) ClearOverride(ref ThreadRef) error {
	overrides, err := s.readOverrides()
	if err != nil {
		return err
	}
	delete(overrides, overrideKey(ref.User, ref.Host, ref.WorkingDir))
	return s.writeOverrides(overrides)
}

func (s Store) AppendTurn(thread Thread, userPrompt string, response core.LLMResponse, results []core.ActionResult, status core.CompletionStatus, opts AppendOptions) Thread {
	turn := ThreadTurn{
		UserPrompt:       strings.TrimSpace(userPrompt),
		AssistantSummary: strings.TrimSpace(response.Summary),
		Findings:         append([]string{}, response.Findings...),
		ActionSummary:    summarizeActions(response.Actions),
		ResultSummary:    summarizeResults(results, opts.ResultCharLimit),
		CompletionStatus: status,
		CreatedAt:        time.Now().UTC(),
	}
	turn.EstimatedTokens = estimateTurnTokens(turn)
	thread.Turns = append(thread.Turns, turn)
	thread = compact(thread, opts.ThreadConfig)
	thread.EstimatedChars = estimateThreadChars(thread)
	thread.EstimatedTokens = estimateTokens(thread.EstimatedChars)
	thread.LastWarning = thread.EstimatedTokens >= int(float64(maxTokens(opts.ThreadConfig))*warnRatio(opts.ThreadConfig))
	return thread
}

func PromptMessages(thread Thread, opts PromptOptions) []core.ConversationMessage {
	messages := []core.ConversationMessage{}
	if strings.TrimSpace(thread.RollingSummary) != "" {
		messages = append(messages, core.ConversationMessage{
			Role:    "assistant",
			Content: "Thread recap:\n" + strings.TrimSpace(thread.RollingSummary),
		})
	}
	for _, turn := range trimTurns(thread.Turns, opts.RecentTurns) {
		messages = append(messages, core.ConversationMessage{
			Role:    "user",
			Content: "Previous user request:\n" + turn.UserPrompt,
		})
		assistantText := buildAssistantReplay(turn)
		if assistantText != "" {
			messages = append(messages, core.ConversationMessage{
				Role:    "assistant",
				Content: assistantText,
			})
		}
	}
	return messages
}

func NewThread(ref ThreadRef) Thread {
	now := time.Now().UTC()
	return Thread{
		ID:          ref.ThreadID,
		ScopeKey:    ref.ScopeKey,
		Scope:       ref.Scope,
		CWD:         ref.WorkingDir,
		Host:        ref.Host,
		User:        ref.User,
		ParentPID:   ref.ParentPID,
		ParentStart: ref.ParentStart,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func compact(thread Thread, cfg cfgpkg.ThreadConfig) Thread {
	if !cfg.AutoCompactValue() {
		return thread
	}
	recentTurns := cfg.RecentTurns
	if recentTurns <= 0 {
		recentTurns = 8
	}
	max := maxTokens(cfg)
	if max <= 0 {
		return thread
	}
	for len(thread.Turns) > recentTurns || estimateTokens(estimateThreadChars(thread)) > max {
		if len(thread.Turns) <= recentTurns && len(thread.Turns) == 0 {
			break
		}
		oldest := thread.Turns[0]
		thread.RollingSummary = mergeSummary(thread.RollingSummary, oldest)
		thread.Turns = append([]ThreadTurn{}, thread.Turns[1:]...)
	}
	return thread
}

func summarizeActions(actions []core.Action) string {
	if len(actions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		target := action.Title
		if target == "" {
			target = action.Command
		}
		if target == "" {
			target = action.Path
		}
		if target == "" {
			target = action.Query
		}
		if target == "" {
			target = string(action.Type)
		}
		parts = append(parts, fmt.Sprintf("%s: %s", action.Type, strings.TrimSpace(target)))
	}
	return strings.Join(parts, "; ")
}

func summarizeResults(results []core.ActionResult, limit int) string {
	if len(results) == 0 {
		return ""
	}
	parts := make([]string, 0, len(results))
	for _, result := range results {
		status := "ok"
		if result.Error != "" {
			status = "error"
		}
		if result.Skipped {
			status = "skipped"
		}
		label := strings.TrimSpace(result.Action.Title)
		if label == "" {
			label = string(result.Action.Type)
		}
		text := result.Summary
		if strings.TrimSpace(text) == "" {
			text = strings.TrimSpace(result.Stdout)
		}
		if strings.TrimSpace(text) == "" {
			text = strings.TrimSpace(result.Stderr)
		}
		text = compressText(text)
		if limit > 0 && len(text) > limit {
			text = text[:limit] + "..."
		}
		if text != "" {
			parts = append(parts, fmt.Sprintf("%s %s: %s", status, label, text))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s", status, label))
		}
	}
	return strings.Join(parts, "; ")
}

func estimateTurnTokens(turn ThreadTurn) int {
	return estimateTokens(len(turn.UserPrompt) + len(turn.AssistantSummary) + len(strings.Join(turn.Findings, " ")) + len(turn.ActionSummary) + len(turn.ResultSummary))
}

func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

func estimateThreadChars(thread Thread) int {
	total := len(thread.RollingSummary)
	for _, turn := range thread.Turns {
		total += len(turn.UserPrompt) + len(turn.AssistantSummary) + len(strings.Join(turn.Findings, " ")) + len(turn.ActionSummary) + len(turn.ResultSummary)
	}
	return total
}

func trimTurns(turns []ThreadTurn, recentTurns int) []ThreadTurn {
	if recentTurns <= 0 || len(turns) <= recentTurns {
		return append([]ThreadTurn{}, turns...)
	}
	return append([]ThreadTurn{}, turns[len(turns)-recentTurns:]...)
}

func buildAssistantReplay(turn ThreadTurn) string {
	lines := []string{}
	if turn.AssistantSummary != "" {
		lines = append(lines, "Previous assistant summary:\n"+turn.AssistantSummary)
	}
	if len(turn.Findings) > 0 {
		lines = append(lines, "Findings: "+strings.Join(turn.Findings, " | "))
	}
	if turn.ActionSummary != "" {
		lines = append(lines, "Actions: "+turn.ActionSummary)
	}
	if turn.ResultSummary != "" {
		lines = append(lines, "Results: "+turn.ResultSummary)
	}
	if turn.CompletionStatus != "" && turn.CompletionStatus != core.CompletionFinished {
		lines = append(lines, "Completion status: "+string(turn.CompletionStatus))
	}
	return strings.Join(lines, "\n")
}

func mergeSummary(existing string, turn ThreadTurn) string {
	entry := []string{"- User: " + compressText(turn.UserPrompt)}
	if turn.AssistantSummary != "" {
		entry = append(entry, "Assistant: "+compressText(turn.AssistantSummary))
	}
	if turn.ResultSummary != "" {
		entry = append(entry, "Outcome: "+compressText(turn.ResultSummary))
	}
	if turn.CompletionStatus != "" && turn.CompletionStatus != core.CompletionFinished {
		entry = append(entry, "Status: "+string(turn.CompletionStatus))
	}
	addition := strings.Join(entry, " | ")
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	return existing + "\n" + addition
}

func previewMessageCount(thread Thread, cfg cfgpkg.ThreadConfig) int {
	count := 0
	if strings.TrimSpace(thread.RollingSummary) != "" {
		count++
	}
	for _, turn := range trimTurns(thread.Turns, cfg.RecentTurns) {
		count++
		if buildAssistantReplay(turn) != "" {
			count++
		}
	}
	return count
}

func summarizeRecentTurn(turn ThreadTurn) string {
	return fmt.Sprintf("user=%q assistant=%q", truncate(turn.UserPrompt, 48), truncate(turn.AssistantSummary, 48))
}

func (s Store) threadPath(threadID string) string {
	return filepath.Join(s.Dir, threadID+".json")
}

func parentStartTime(pid int) (string, error) {
	if pid <= 1 {
		return "", nil
	}
	cmd := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprintf("%d", pid))
	output, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	data, readErr := os.ReadFile(filepath.Join("/proc", fmt.Sprintf("%d", pid), "stat"))
	if readErr == nil {
		return strings.TrimSpace(string(data)), nil
	}
	return "", err
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func overrideKey(userName, host, cwd string) string {
	return shortHash(userName + "|" + host + "|" + cwd)
}

func loadOverride(dir, userName, host, cwd string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "overrides.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read thread overrides: %w", err)
	}
	records := map[string]OverrideRecord{}
	if err := json.Unmarshal(data, &records); err != nil {
		return "", false, fmt.Errorf("unmarshal thread overrides: %w", err)
	}
	record, ok := records[overrideKey(userName, host, cwd)]
	if !ok {
		return "", false, nil
	}
	return record.ThreadID, true, nil
}

func (s Store) readOverrides() (map[string]OverrideRecord, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, "overrides.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]OverrideRecord{}, nil
		}
		return nil, fmt.Errorf("read thread overrides: %w", err)
	}
	records := map[string]OverrideRecord{}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("unmarshal thread overrides: %w", err)
	}
	return records, nil
}

func (s Store) writeOverrides(records map[string]OverrideRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal thread overrides: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "overrides.json"), data, 0o600); err != nil {
		return fmt.Errorf("write thread overrides: %w", err)
	}
	return nil
}

func compressText(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return truncate(text, 220)
}

func truncate(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	if max <= 3 {
		return text[:max]
	}
	return text[:max-3] + "..."
}

func warnRatio(cfg cfgpkg.ThreadConfig) float64 {
	if cfg.WarnAtRatio <= 0 || cfg.WarnAtRatio >= 1 {
		return 0.8
	}
	return cfg.WarnAtRatio
}

func maxTokens(cfg cfgpkg.ThreadConfig) int {
	if cfg.MaxTokens <= 0 {
		return 12000
	}
	return cfg.MaxTokens
}
