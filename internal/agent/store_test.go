package agent

import "testing"

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
	workspace := BuildWorkspace("IronLark", "richardson", "srv-1", "/opt/very-long-service-name-for-agent", "thread-1", false)

	if workspace.TmuxSession == "" || workspace.TmuxWindow == "" || workspace.TmuxTarget == "" {
		t.Fatalf("expected tmux metadata to be populated: %#v", workspace)
	}
	if workspace.TmuxTarget != workspace.TmuxSession+":"+workspace.TmuxWindow {
		t.Fatalf("unexpected tmux target %q", workspace.TmuxTarget)
	}
	if workspace.ThreadID != "thread-1" {
		t.Fatalf("expected thread id to be preserved, got %q", workspace.ThreadID)
	}
}
