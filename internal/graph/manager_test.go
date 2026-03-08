package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	cfgpkg "github.com/richardsondx/IronLark/internal/config"
)

func TestBuildPlanSkipsUnavailableSubsystems(t *testing.T) {
	manager := NewManager(Store{Dir: t.TempDir()}, cfgpkg.DefaultConfig().Graph, t.TempDir())
	manager.Host = "srv-1"
	manager.User = "richardson"

	plan := manager.BuildPlan(HostCapabilities{
		PSAvailable:      true,
		SSAvailable:      true,
		SystemdAvailable: false,
		DockerAvailable:  false,
		GitAvailable:     false,
	}, GraphSnapshot{}, ModeLight)

	reasons := map[string]string{}
	for _, selection := range plan.Selections {
		if selection.Skipped {
			reasons[selection.Name] = selection.Reason
		}
	}
	if reasons["docker"] != "docker is unavailable" {
		t.Fatalf("expected docker skip reason, got %q", reasons["docker"])
	}
	if reasons["service"] != "systemctl is unavailable" {
		t.Fatalf("expected service skip reason, got %q", reasons["service"])
	}
	if reasons["git"] != "no git repository detected" {
		t.Fatalf("expected git skip reason, got %q", reasons["git"])
	}
}

func TestSummaryIncludesSkippedCrawlerHighlights(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(Store{Dir: dir}, cfgpkg.DefaultConfig().Graph, dir)
	manager.Host = "srv-1"
	manager.User = "richardson"
	now := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	manager.Now = func() time.Time { return now }

	snapshot := GraphSnapshot{
		ID:          "snap-1",
		Host:        manager.HostKey(),
		CollectedAt: now,
		Coverage: []CrawlerSelection{
			{Name: "docker", Enabled: true, Skipped: true, Reason: "docker is unavailable"},
		},
		Services:  []Service{{Name: "nginx.service", ActiveState: "active"}},
		Processes: []Process{{PID: 1, Command: "systemd"}},
		Listeners: []Listener{{Port: 80, Proto: "tcp"}},
	}
	if err := manager.Store.SaveSnapshot(snapshot, []GraphEvent{{
		ID:         "evt-1",
		SnapshotID: snapshot.ID,
		Host:       snapshot.Host,
		OccurredAt: now,
		Type:       "service.appeared",
		Severity:   "info",
		Summary:    "service appeared: nginx.service:active",
	}}); err != nil {
		t.Fatal(err)
	}

	summary, err := manager.Summary(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, highlight := range summary.Highlights {
		if highlight == "docker skipped: docker is unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skipped crawler highlight, got %#v", summary.Highlights)
	}
}

func TestCrawlCapturesFileChangeEvent(t *testing.T) {
	dir := t.TempDir()
	config := cfgpkg.DefaultConfig().Graph
	manager := NewManager(Store{Dir: filepath.Join(dir, "graph")}, config, dir)
	manager.Host = "srv-1"
	manager.User = "richardson"
	now := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	manager.Now = func() time.Time { return now }
	manager.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		switch name {
		case "sh":
			command := args[len(args)-1]
			switch {
			case command == "command -v docker >/dev/null 2>&1 && echo yes || echo no":
				return "no\n", nil
			case command == "command -v systemctl >/dev/null 2>&1 && echo yes || echo no":
				return "no\n", nil
			case command == "command -v journalctl >/dev/null 2>&1 && echo yes || echo no":
				return "no\n", nil
			case command == "command -v git >/dev/null 2>&1 && echo yes || echo no":
				return "no\n", nil
			case command == "command -v ss >/dev/null 2>&1 && echo yes || echo no":
				return "yes\n", nil
			case command == "command -v ps >/dev/null 2>&1 && echo yes || echo no":
				return "yes\n", nil
			case command == "ps -eo pid=,ppid=,user=,comm= | head -n 25":
				return "1 0 root init\n", nil
			case command == "ss -ltnpH 2>/dev/null | head -n 25":
				return "tcp LISTEN 0 128 0.0.0.0:3000 0.0.0.0:* users:((\"node\",pid=123,fd=18))\n", nil
			}
		}
		return "", nil
	}

	filePath := filepath.Join(dir, "package.json")
	if err := os.WriteFile(filePath, []byte(`{"name":"app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first, _, err := manager.Crawl(context.Background(), ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	_ = first
	now = now.Add(10 * time.Minute)
	if err := os.WriteFile(filePath, []byte(`{"name":"app","version":"1.0.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second, events, err := manager.Crawl(context.Background(), ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == "" {
		t.Fatal("expected second snapshot id")
	}
	found := false
	for _, event := range events {
		if event.Type == "file.changed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected file changed event, got %#v", events)
	}
}
