package threads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/state"
)

func TestResolveDefaultThreadSameShellSameCWD(t *testing.T) {
	t.Cleanup(func() { lookupParentStart = parentStartTime })
	lookupParentStart = func(pid int) (string, error) { return "Mon Jan 2 15:04:05 2006", nil }

	dir := t.TempDir()
	runtime := state.Runtime{
		Config:     cfgpkg.DefaultConfig(),
		Paths:      cfgpkg.Paths{ThreadsDir: dir},
		WorkingDir: "/tmp/project-a",
	}

	a, err := ResolveDefaultThread(runtime)
	if err != nil {
		t.Fatalf("ResolveDefaultThread() error = %v", err)
	}
	b, err := ResolveDefaultThread(runtime)
	if err != nil {
		t.Fatalf("ResolveDefaultThread() second error = %v", err)
	}
	if a.ThreadID != b.ThreadID {
		t.Fatalf("expected same thread id, got %q and %q", a.ThreadID, b.ThreadID)
	}
	if a.Source != "auto-shell" {
		t.Fatalf("expected auto-shell source, got %q", a.Source)
	}
}

func TestResolveDefaultThreadDifferentCWD(t *testing.T) {
	t.Cleanup(func() { lookupParentStart = parentStartTime })
	lookupParentStart = func(pid int) (string, error) { return "Mon Jan 2 15:04:05 2006", nil }

	cfg := cfgpkg.DefaultConfig()
	a, err := ResolveDefaultThread(state.Runtime{
		Config:     cfg,
		Paths:      cfgpkg.Paths{ThreadsDir: t.TempDir()},
		WorkingDir: "/tmp/project-a",
	})
	if err != nil {
		t.Fatalf("ResolveDefaultThread(a) error = %v", err)
	}
	b, err := ResolveDefaultThread(state.Runtime{
		Config:     cfg,
		Paths:      cfgpkg.Paths{ThreadsDir: t.TempDir()},
		WorkingDir: "/tmp/project-b",
	})
	if err != nil {
		t.Fatalf("ResolveDefaultThread(b) error = %v", err)
	}
	if a.ThreadID == b.ThreadID {
		t.Fatalf("expected different thread ids for different cwd, got %q", a.ThreadID)
	}
}

func TestResolveDefaultThreadFallsBackToCWD(t *testing.T) {
	t.Cleanup(func() { lookupParentStart = parentStartTime })
	lookupParentStart = func(pid int) (string, error) { return "", os.ErrNotExist }

	ref, err := ResolveDefaultThread(state.Runtime{
		Config:     cfgpkg.DefaultConfig(),
		Paths:      cfgpkg.Paths{ThreadsDir: t.TempDir()},
		WorkingDir: "/tmp/project-a",
	})
	if err != nil {
		t.Fatalf("ResolveDefaultThread() error = %v", err)
	}
	if !ref.Degraded {
		t.Fatalf("expected degraded fallback")
	}
	if ref.Source != "cwd-fallback" {
		t.Fatalf("expected cwd-fallback source, got %q", ref.Source)
	}
}

func TestPromptMessagesAndCompaction(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	cfg := cfgpkg.DefaultConfig()
	cfg.Thread.MaxTokens = 60
	cfg.Thread.RecentTurns = 2

	thread := NewThread(ThreadRef{
		ThreadID:   "thread-1",
		ScopeKey:   "scope",
		Scope:      ScopeAutoShell,
		WorkingDir: "/tmp/project",
		Host:       "host",
		User:       "user",
	})

	for i := 0; i < 4; i++ {
		thread = store.AppendTurn(thread, strings.Repeat("user prompt ", 8), core.LLMResponse{
			Summary:  strings.Repeat("assistant summary ", 8),
			Findings: []string{"finding"},
		}, []core.ActionResult{{
			Action:  core.Action{Title: "inspect"},
			Summary: strings.Repeat("result output ", 10),
		}}, AppendOptions{
			ResultCharLimit: 80,
			ThreadConfig:    cfg.Thread,
		})
	}

	if len(thread.Turns) > cfg.Thread.RecentTurns {
		t.Fatalf("expected compaction to keep at most %d turns, got %d", cfg.Thread.RecentTurns, len(thread.Turns))
	}
	if thread.RollingSummary == "" {
		t.Fatalf("expected rolling summary after compaction")
	}
	if !thread.LastWarning {
		t.Fatalf("expected warning state after compaction")
	}

	messages := PromptMessages(thread, PromptOptions{RecentTurns: cfg.Thread.RecentTurns})
	if len(messages) == 0 {
		t.Fatalf("expected replay messages")
	}
	if messages[0].Role != "assistant" || !strings.Contains(messages[0].Content, "Thread recap") {
		t.Fatalf("expected thread recap assistant message, got %#v", messages[0])
	}
}

func TestStoreClearDeleteAndOverride(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	thread := NewThread(ThreadRef{
		ThreadID:   "thread-1",
		ScopeKey:   "scope",
		Scope:      ScopeCWD,
		WorkingDir: "/tmp/project",
		Host:       "host",
		User:       "user",
	})
	thread.Turns = []ThreadTurn{{UserPrompt: "hello", CreatedAt: time.Now().UTC()}}
	if err := store.Save(thread); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.Clear(thread.ID); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	loaded, err := store.Load(thread.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Turns) != 0 || loaded.RollingSummary != "" {
		t.Fatalf("expected cleared thread, got %#v", loaded)
	}

	ref := ThreadRef{ThreadID: "manual-thread", WorkingDir: "/tmp/project", Host: "host", User: "user"}
	if err := store.UpsertOverride(ref); err != nil {
		t.Fatalf("UpsertOverride() error = %v", err)
	}
	overrideData, err := os.ReadFile(filepath.Join(dir, "overrides.json"))
	if err != nil {
		t.Fatalf("read override file: %v", err)
	}
	if !strings.Contains(string(overrideData), "manual-thread") {
		t.Fatalf("expected override file to contain thread id")
	}
	if err := store.Delete(thread.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, thread.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("expected thread file deleted, stat err=%v", err)
	}
}
