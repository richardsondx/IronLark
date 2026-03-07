package checkpoints

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndRestoreCheckpoint(t *testing.T) {
	workdir := t.TempDir()
	target := filepath.Join(workdir, "config.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := Store{Dir: filepath.Join(workdir, "checkpoints")}
	record, err := store.Create([]string{target}, "before mutation")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Restore(record.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\n" {
		t.Fatalf("expected restored content, got %q", string(data))
	}
}

func TestRestoreRemovesNewFile(t *testing.T) {
	workdir := t.TempDir()
	target := filepath.Join(workdir, "new.txt")
	store := Store{Dir: filepath.Join(workdir, "checkpoints")}
	record, err := store.Create([]string{target}, "before create")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("created later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Restore(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, got %v", err)
	}
}
