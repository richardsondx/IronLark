package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Store struct {
	Dir string
}

func (s Store) hostDir(hostKey string) string {
	return filepath.Join(s.Dir, sanitizeKey(hostKey))
}

func (s Store) latestPath(hostKey string) string {
	return filepath.Join(s.hostDir(hostKey), "latest.json")
}

func (s Store) eventsPath(hostKey string) string {
	return filepath.Join(s.hostDir(hostKey), "events.json")
}

func (s Store) watchPath(hostKey string) string {
	return filepath.Join(s.hostDir(hostKey), "watch.json")
}

func (s Store) SaveSnapshot(snapshot GraphSnapshot, events []GraphEvent) error {
	if strings.TrimSpace(snapshot.Host) == "" {
		return fmt.Errorf("snapshot host is required")
	}
	if err := os.MkdirAll(s.hostDir(snapshot.Host), 0o755); err != nil {
		return fmt.Errorf("create host graph directory: %w", err)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := os.WriteFile(s.latestPath(snapshot.Host), data, 0o600); err != nil {
		return fmt.Errorf("write latest snapshot: %w", err)
	}
	if len(events) > 0 {
		existing, err := s.Events(snapshot.Host)
		if err != nil {
			return err
		}
		existing = append(existing, events...)
		sort.Slice(existing, func(i, j int) bool {
			return existing[i].OccurredAt.Before(existing[j].OccurredAt)
		})
		eventsData, err := json.MarshalIndent(existing, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal events: %w", err)
		}
		if err := os.WriteFile(s.eventsPath(snapshot.Host), eventsData, 0o600); err != nil {
			return fmt.Errorf("write events: %w", err)
		}
	}
	return nil
}

func (s Store) Latest(hostKey string) (GraphSnapshot, error) {
	data, err := os.ReadFile(s.latestPath(hostKey))
	if err != nil {
		if os.IsNotExist(err) {
			return GraphSnapshot{}, nil
		}
		return GraphSnapshot{}, fmt.Errorf("read latest snapshot: %w", err)
	}
	var snapshot GraphSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return GraphSnapshot{}, fmt.Errorf("decode latest snapshot: %w", err)
	}
	return snapshot, nil
}

func (s Store) Events(hostKey string) ([]GraphEvent, error) {
	data, err := os.ReadFile(s.eventsPath(hostKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read events: %w", err)
	}
	var events []GraphEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return events, nil
}

func (s Store) EventsSince(hostKey string, since time.Time) ([]GraphEvent, error) {
	events, err := s.Events(hostKey)
	if err != nil {
		return nil, err
	}
	if since.IsZero() {
		return events, nil
	}
	filtered := make([]GraphEvent, 0, len(events))
	for _, event := range events {
		if event.OccurredAt.After(since) || event.OccurredAt.Equal(since) {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

func (s Store) SaveWatch(hostKey string, watch WatchState) error {
	if err := os.MkdirAll(s.hostDir(hostKey), 0o755); err != nil {
		return fmt.Errorf("create host graph directory: %w", err)
	}
	data, err := json.MarshalIndent(watch, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal watch state: %w", err)
	}
	if err := os.WriteFile(s.watchPath(hostKey), data, 0o600); err != nil {
		return fmt.Errorf("write watch state: %w", err)
	}
	return nil
}

func (s Store) Watch(hostKey string) (WatchState, error) {
	data, err := os.ReadFile(s.watchPath(hostKey))
	if err != nil {
		if os.IsNotExist(err) {
			return WatchState{}, nil
		}
		return WatchState{}, fmt.Errorf("read watch state: %w", err)
	}
	var watch WatchState
	if err := json.Unmarshal(data, &watch); err != nil {
		return WatchState{}, fmt.Errorf("decode watch state: %w", err)
	}
	return watch, nil
}

func sanitizeKey(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, string(filepath.Separator), "_")
	value = strings.ReplaceAll(value, " ", "_")
	if value == "" {
		return "default"
	}
	return value
}
