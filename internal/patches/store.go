package patches

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Record struct {
	ID         string    `json:"id"`
	Path       string    `json:"path"`
	BackupPath string    `json:"backup_path"`
	Diff       string    `json:"diff"`
	CreatedAt  time.Time `json:"created_at"`
}

type Store struct {
	Dir string
}

func (s Store) Apply(path string, diff string) (Record, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Record{}, fmt.Errorf("create patch directory: %w", err)
	}

	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Record{}, fmt.Errorf("read original file: %w", err)
	}

	updated, err := ApplyUnifiedDiff(string(original), diff)
	if err != nil {
		return Record{}, err
	}

	id := time.Now().UTC().Format("20060102T150405.000000000")
	record := Record{
		ID:         id,
		Path:       path,
		BackupPath: filepath.Join(s.Dir, id+".bak"),
		Diff:       diff,
		CreatedAt:  time.Now().UTC(),
	}

	if err := os.WriteFile(record.BackupPath, original, 0o600); err != nil {
		return Record{}, fmt.Errorf("write patch backup: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Record{}, fmt.Errorf("create target directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return Record{}, fmt.Errorf("write patched file: %w", err)
	}
	if err := s.writeRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s Store) Undo(id string) (Record, error) {
	record, err := s.Get(id)
	if err != nil {
		return Record{}, err
	}
	backup, err := os.ReadFile(record.BackupPath)
	if err != nil {
		return Record{}, fmt.Errorf("read backup: %w", err)
	}
	if err := os.WriteFile(record.Path, backup, 0o644); err != nil {
		return Record{}, fmt.Errorf("restore file: %w", err)
	}
	return record, nil
}

func (s Store) Get(id string) (Record, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return Record{}, fmt.Errorf("read patch record: %w", err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("decode patch record: %w", err)
	}
	return record, nil
}

func (s Store) List() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list patch records: %w", err)
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
	return records, nil
}

func (s Store) writeRecord(record Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal patch record: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, record.ID+".json"), data, 0o600); err != nil {
		return fmt.Errorf("write patch record: %w", err)
	}
	return nil
}
