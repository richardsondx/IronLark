package checkpoints

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileSnapshot struct {
	Path       string `json:"path"`
	BackupPath string `json:"backup_path"`
	Existed    bool   `json:"existed"`
}

type Record struct {
	ID        string         `json:"id"`
	Reason    string         `json:"reason,omitempty"`
	Files     []FileSnapshot `json:"files"`
	CreatedAt time.Time      `json:"created_at"`
}

type Store struct {
	Dir string
}

func (s Store) Create(paths []string, reason string) (Record, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Record{}, fmt.Errorf("create checkpoint directory: %w", err)
	}
	id := time.Now().UTC().Format("20060102T150405.000000000")
	record := Record{
		ID:        id,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		existed := true
		if err != nil {
			if !os.IsNotExist(err) {
				return Record{}, fmt.Errorf("read checkpoint target: %w", err)
			}
			existed = false
			data = nil
		}
		backupPath := filepath.Join(s.Dir, id+"-"+filepath.Base(path)+".bak")
		if err := os.WriteFile(backupPath, data, 0o600); err != nil {
			return Record{}, fmt.Errorf("write checkpoint backup: %w", err)
		}
		record.Files = append(record.Files, FileSnapshot{
			Path:       path,
			BackupPath: backupPath,
			Existed:    existed,
		})
	}
	if err := s.writeRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s Store) Restore(id string) (Record, error) {
	record, err := s.Get(id)
	if err != nil {
		return Record{}, err
	}
	for _, snapshot := range record.Files {
		data, err := os.ReadFile(snapshot.BackupPath)
		if err != nil {
			return Record{}, fmt.Errorf("read checkpoint backup: %w", err)
		}
		if !snapshot.Existed {
			if err := os.Remove(snapshot.Path); err != nil && !os.IsNotExist(err) {
				return Record{}, fmt.Errorf("remove checkpoint target: %w", err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(snapshot.Path), 0o755); err != nil {
			return Record{}, fmt.Errorf("restore checkpoint path: %w", err)
		}
		if err := os.WriteFile(snapshot.Path, data, 0o644); err != nil {
			return Record{}, fmt.Errorf("restore checkpoint file: %w", err)
		}
	}
	return record, nil
}

func (s Store) Get(id string) (Record, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return Record{}, fmt.Errorf("read checkpoint record: %w", err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("decode checkpoint record: %w", err)
	}
	return record, nil
}

func (s Store) List() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	records := []Record{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := s.Get(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	return records, nil
}

func (s Store) writeRecord(record Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint record: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, record.ID+".json"), data, 0o600); err != nil {
		return fmt.Errorf("write checkpoint record: %w", err)
	}
	return nil
}
