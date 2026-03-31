package taskruntime

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

func TestStoreSavesAndListsNewestFirst(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "tasks")}
	first := NewActionRecord(core.Action{ID: "1", Type: core.ActionRunShell, Title: "first"}, "shell.inline")
	second := NewActionRecord(core.Action{ID: "2", Type: core.ActionReadFiles, Title: "second"}, "files.read")
	second.StartedAt = first.StartedAt.Add(time.Nanosecond)
	first.State = StateCompleted
	second.State = StateRunning
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	records, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].ID != second.ID {
		t.Fatalf("expected newest record first, got %#v", records)
	}
}
