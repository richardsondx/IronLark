package ops

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
	"github.com/richardsondx/IronLark/internal/graph"
)

func TestResolveEntityPrefersSystemdServiceOverOtherMatches(t *testing.T) {
	snapshot := graph.GraphSnapshot{
		Services: []graph.Service{
			{Name: "openclaw.service", ActiveState: "active"},
		},
		Containers: []graph.Container{
			{Name: "openclaw", Image: "openclaw:latest"},
		},
		Listeners: []graph.Listener{
			{Port: 3000, Process: "openclaw"},
		},
		Relations: []graph.GraphRelation{
			{From: "service:openclaw.service", To: "port:3000", Type: "listens_on"},
		},
	}

	entity, err := ResolveEntity(snapshot, "openclaw")
	if err != nil {
		t.Fatalf("ResolveEntity() error = %v", err)
	}
	if entity.Kind != EntityService {
		t.Fatalf("expected service entity, got %#v", entity)
	}
	if entity.Manager != ManagerSystemd {
		t.Fatalf("expected systemd manager, got %#v", entity)
	}
	if entity.Port != 3000 {
		t.Fatalf("expected inferred port 3000, got %#v", entity)
	}
}

func TestSummaryLineIncludesShellRuns(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(cfgpkg.Paths{
		OpsDir:       filepath.Join(root, "ops"),
		WatchersDir:  filepath.Join(root, "ops", "watchers"),
		RunsDir:      filepath.Join(root, "ops", "runs"),
		ShellRunsDir: filepath.Join(root, "ops", "shell-runs"),
		IncidentsDir: filepath.Join(root, "ops", "incidents"),
		ProcessesDir: filepath.Join(root, "ops", "processes"),
	})
	if err := manager.SaveProcess(ProcessRecord{
		ID:              "shell-1",
		Kind:            ProcessShellRun,
		PID:             os.Getpid(),
		State:           "running",
		StartedAt:       time.Now().UTC(),
		LastHeartbeatAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := manager.SummaryLine(); got != "ops: 1 run" {
		t.Fatalf("expected shell run in summary, got %q", got)
	}
}
