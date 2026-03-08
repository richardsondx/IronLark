package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceKeyStablePerHostAndCWD(t *testing.T) {
	first := WorkspaceKey("srv-1", "/opt/app")
	second := WorkspaceKey("srv-1", "/opt/app")
	third := WorkspaceKey("srv-2", "/opt/app")

	if first != second {
		t.Fatalf("expected stable key, got %q and %q", first, second)
	}
	if first == third {
		t.Fatalf("expected host to affect key, got %q", first)
	}
}

func TestBuildWorkspaceUsesPrefixAndTarget(t *testing.T) {
	workspace := BuildWorkspace("IronLark", "/tmp/agents", "richardson", "srv-1", "/opt/very-long-service-name-for-agent", "thread-1")

	if workspace.SessionID == "" || workspace.SocketPath == "" {
		t.Fatalf("expected session metadata to be populated: %#v", workspace)
	}
	if workspace.SessionID != "ironlark-"+workspace.Key {
		t.Fatalf("unexpected session id %q", workspace.SessionID)
	}
	if workspace.SocketPath != "/tmp/agents/"+workspace.Key+".sock" {
		t.Fatalf("unexpected socket path %q", workspace.SocketPath)
	}
	if workspace.ThreadID != "thread-1" {
		t.Fatalf("expected thread id to be preserved, got %q", workspace.ThreadID)
	}
}

func TestLoadBackfillsSessionMetadataForLegacyWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abc123.json"), []byte(`{"key":"abc123","cwd":"/opt/app"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	workspace, err := (Store{Dir: dir}).Load("abc123")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if workspace.SessionID != "ironlark-abc123" {
		t.Fatalf("expected session id backfill, got %q", workspace.SessionID)
	}
	if workspace.SocketPath != filepath.Join(dir, "abc123.sock") {
		t.Fatalf("expected socket path backfill, got %q", workspace.SocketPath)
	}
	if workspace.State != StateStopped {
		t.Fatalf("expected default stopped state, got %q", workspace.State)
	}
}
