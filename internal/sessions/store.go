package sessions

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

type Record struct {
	ID               string                     `json:"id"`
	Mode             string                     `json:"mode"`
	Prompt           string                     `json:"prompt"`
	Provider         string                     `json:"provider"`
	Model            string                     `json:"model"`
	ApprovalMode     core.ApprovalMode          `json:"approval_mode"`
	ContextJSON      string                     `json:"context_json,omitempty"`
	Findings         []string                   `json:"findings,omitempty"`
	Actions          []core.Action              `json:"actions,omitempty"`
	Results          []core.ActionResult        `json:"results,omitempty"`
	Summary          string                     `json:"summary,omitempty"`
	Messages         []core.ConversationMessage `json:"messages,omitempty"`
	NeedsUserInput   bool                       `json:"needs_user_input,omitempty"`
	Confidence       float64                    `json:"confidence,omitempty"`
	CompletionStatus core.CompletionStatus      `json:"completion_status,omitempty"`
	RequestCount     int                        `json:"request_count,omitempty"`
	TokenUsage       core.TokenUsage            `json:"token_usage,omitempty"`
	StartedAt        time.Time                  `json:"started_at"`
	FinishedAt       time.Time                  `json:"finished_at"`
}

type Store struct {
	Dir string
}

func (s Store) Save(record Record) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	if record.ID == "" {
		record.ID = time.Now().UTC().Format("20060102T150405.000000000")
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	if record.FinishedAt.IsZero() {
		record.FinishedAt = record.StartedAt
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session record: %w", err)
	}
	path := filepath.Join(s.Dir, record.ID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write session record: %w", err)
	}
	return nil
}

func (s Store) List() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	records := []Record{}
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
	return records, nil
}
