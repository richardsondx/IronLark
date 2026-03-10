package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/richardsondx/IronLark/internal/agent"
	"github.com/richardsondx/IronLark/internal/ops"
)

func TestPSHealthForStaleAgentIsOrphaned(t *testing.T) {
	now := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	entry := psEntry{
		Kind:            string(ops.ProcessAgent),
		State:           agent.StateStale,
		LastHeartbeatAt: now.Add(-10 * time.Second),
	}

	if got := psHealth(entry, now); got != "orphaned" {
		t.Fatalf("psHealth() = %q, want orphaned", got)
	}
}

func TestPSHealthMarksOldRecoveryAsStuck(t *testing.T) {
	now := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	entry := psEntry{
		Kind:            string(ops.ProcessRecovery),
		State:           "running",
		LastHeartbeatAt: now.Add(-6 * time.Minute),
	}

	if got := psHealth(entry, now); got != "stuck?" {
		t.Fatalf("psHealth() = %q, want stuck?", got)
	}
}

func TestRenderPSEntriesIncludesHealthColumn(t *testing.T) {
	lines := renderPSEntries([]psEntry{
		{
			ID:           "fecce4e3780e",
			Kind:         string(ops.ProcessAgent),
			State:        agent.StateStale,
			Health:       "orphaned",
			PID:          8170,
			Age:          "5h",
			LastActivity: "9s ago",
			TokenUsage:   "n/a",
			Target:       "/Users/richardson/Code",
		},
	})

	if len(lines) != 2 {
		t.Fatalf("renderPSEntries() produced %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "HEALTH") {
		t.Fatalf("header %q does not include HEALTH", lines[0])
	}
	if !strings.Contains(lines[1], "orphaned") {
		t.Fatalf("row %q does not include health value", lines[1])
	}
	if !strings.Contains(lines[1], "/Users/richardson/Code") {
		t.Fatalf("row %q does not include target", lines[1])
	}
}
