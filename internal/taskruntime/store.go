package taskruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

type Kind string

const (
	KindInlineAction  Kind = "inline_action"
	KindBackgroundRun Kind = "background_run"
	KindWatcher       Kind = "watcher"
	KindRecovery      Kind = "recovery"
	KindSubagent      Kind = "subagent"
)

type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateBlocked   State = "blocked"
	StateSkipped   State = "skipped"
	StateCanceled  State = "canceled"
)

type Record struct {
	ID              string          `json:"id"`
	Kind            Kind            `json:"kind"`
	State           State           `json:"state"`
	ActionID        string          `json:"action_id,omitempty"`
	ActionType      core.ActionType `json:"action_type,omitempty"`
	Handler         string          `json:"handler,omitempty"`
	Title           string          `json:"title,omitempty"`
	Target          string          `json:"target,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	Error           string          `json:"error,omitempty"`
	BackgroundRunID string          `json:"background_run_id,omitempty"`
	StartedAt       time.Time       `json:"started_at"`
	FinishedAt      time.Time       `json:"finished_at,omitempty"`
}

type Store struct {
	Dir string
}

func NewActionRecord(action core.Action, handler string) Record {
	return Record{
		ID:         fmt.Sprintf("task-%d", time.Now().UTC().UnixNano()),
		Kind:       KindInlineAction,
		State:      StatePending,
		ActionID:   action.ID,
		ActionType: action.Type,
		Handler:    handler,
		Title:      firstNonEmpty(strings.TrimSpace(action.Title), string(action.Type)),
		Target:     firstNonEmpty(action.Command, action.Path, action.Query),
		StartedAt:  time.Now().UTC(),
	}
}

func (s Store) Save(record Record) error {
	if strings.TrimSpace(s.Dir) == "" {
		return nil
	}
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("task id is required")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create task directory: %w", err)
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task record: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, sanitizeID(record.ID)+".json"), data, 0o600); err != nil {
		return fmt.Errorf("write task record: %w", err)
	}
	return nil
}

func (s Store) List(limit int) ([]Record, error) {
	if strings.TrimSpace(s.Dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list task records: %w", err)
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, entry.Name()))
		if err != nil {
			continue
		}
		var record Record
		if err := json.Unmarshal(data, &record); err == nil {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].StartedAt.After(records[j].StartedAt)
	})
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-")
	return replacer.Replace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
