package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeLauncher struct {
	pid       int
	calls     int
	lastExec  string
	lastArgs  []string
	launchErr error
}

func (f *fakeLauncher) Launch(workspace Workspace, executable string, args []string) (int, error) {
	f.calls++
	f.lastExec = executable
	f.lastArgs = append([]string(nil), args...)
	return f.pid, f.launchErr
}

type fakeControl struct {
	statuses    []controlResponse
	statusErrs  []error
	statusCalls int
	stopCalls   int
}

func (f *fakeControl) Status(ctx context.Context, socketPath string) (controlResponse, error) {
	f.statusCalls++
	if len(f.statusErrs) > 0 {
		err := f.statusErrs[0]
		if len(f.statusErrs) > 1 {
			f.statusErrs = f.statusErrs[1:]
		}
		if err != nil {
			return controlResponse{}, err
		}
	}
	if len(f.statuses) == 0 {
		return controlResponse{}, fmt.Errorf("no status")
	}
	response := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return response, nil
}

func (f *fakeControl) Stop(ctx context.Context, socketPath string) error {
	f.stopCalls++
	return nil
}

func (f *fakeControl) Resize(ctx context.Context, socketPath string, rows, cols int) error {
	return nil
}

func (f *fakeControl) Attach(socketPath string) (net.Conn, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestEnsureWorkspaceStartsRunnerWhenSessionIsMissing(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	workspace := BuildWorkspace("ironlark", dir, "richardson", "srv-1", "/opt/app", "thread-1")
	launcher := &fakeLauncher{pid: 4321}
	control := &fakeControl{
		statusErrs: []error{fmt.Errorf("missing"), nil},
		statuses:   []controlResponse{{OK: true, State: StateLive, RunnerPID: 4321}},
	}
	manager := SessionManager{
		Store:        store,
		Launcher:     launcher,
		Control:      control,
		StartTimeout: time.Second,
		PollInterval: time.Millisecond,
	}

	started, err := manager.EnsureWorkspace(context.Background(), workspace, "/bin/lk", []string{"agent", "__agent-runner"})
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if launcher.calls != 1 {
		t.Fatalf("expected launcher to be called once, got %d", launcher.calls)
	}
	if started.RunnerPID != 4321 || started.State != StateLive {
		t.Fatalf("unexpected started workspace %#v", started)
	}
	saved, err := store.Load(workspace.Key)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if saved.RunnerPID != 4321 || saved.State != StateLive {
		t.Fatalf("expected live workspace to be saved, got %#v", saved)
	}
}

func TestEnsureWorkspaceReusesExistingLiveSession(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	workspace := BuildWorkspace("ironlark", dir, "richardson", "srv-1", "/opt/app", "thread-1")
	launcher := &fakeLauncher{pid: 4321}
	control := &fakeControl{statuses: []controlResponse{{OK: true, State: StateLive, RunnerPID: 1234}}}
	manager := SessionManager{Store: store, Launcher: launcher, Control: control}

	live, err := manager.EnsureWorkspace(context.Background(), workspace, "/bin/lk", []string{"agent"})
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if launcher.calls != 0 {
		t.Fatalf("expected live session to be reused without launching, got %d launches", launcher.calls)
	}
	if live.RunnerPID != 1234 || live.State != StateLive {
		t.Fatalf("unexpected live workspace %#v", live)
	}
}

func TestInspectMarksWorkspaceStaleWhenSocketAndProcessAreGone(t *testing.T) {
	dir := t.TempDir()
	workspace := BuildWorkspace("ironlark", dir, "richardson", "srv-1", "/opt/app", "thread-1")
	workspace.RunnerPID = os.Getpid() + 100000
	manager := SessionManager{
		Store:   Store{Dir: dir},
		Control: &fakeControl{statusErrs: []error{fmt.Errorf("dial unix %s: no such file or directory", workspace.SocketPath)}},
	}

	inspected, err := manager.Inspect(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspected.State != StateStale {
		t.Fatalf("expected stale workspace, got %#v", inspected)
	}
}

func TestSessionSocketPathUsesAgentDirAndKey(t *testing.T) {
	path := sessionSocketPath(filepath.Join(string(os.PathSeparator), "tmp", "agents"), "abc123")
	if path != filepath.Join(string(os.PathSeparator), "tmp", "agents", "abc123.sock") {
		t.Fatalf("unexpected socket path %q", path)
	}
}
