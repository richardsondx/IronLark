package memory

import (
	"strings"
	"testing"

	"github.com/richardsondx/IronLark/internal/core"
)

func TestExtractSessionMemoriesCapturesGoalAndResults(t *testing.T) {
	memories := ExtractSessionMemories(
		"debug why nginx is failing",
		core.LLMResponse{
			Summary:  "nginx failed because the port was already in use",
			Findings: []string{"port 80 was occupied by another process"},
		},
		[]core.ActionResult{{
			Action:   core.Action{Type: core.ActionRunShell, Title: "Check listener"},
			Summary:  "found process listening on port 80",
			TaskID:   "task-1",
			Handler:  "shell.inline",
			Approved: true,
		}},
		8,
	)
	if len(memories) == 0 {
		t.Fatal("expected extracted memories")
	}
	joined := strings.Join(memories, "\n")
	if !strings.Contains(joined, "User goal: debug why nginx is failing") {
		t.Fatalf("expected goal memory, got %q", joined)
	}
	if !strings.Contains(joined, "Finding: port 80 was occupied by another process") {
		t.Fatalf("expected finding memory, got %q", joined)
	}
}
