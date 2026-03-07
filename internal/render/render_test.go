package render

import (
	"strings"
	"testing"
)

func TestFormatDiffLineColorsPatchSections(t *testing.T) {
	r := &Renderer{Color: true}

	if got := r.formatDiffLine("+added line"); !strings.Contains(got, ansiGreen) {
		t.Fatalf("expected added line to be green, got %q", got)
	}
	if got := r.formatDiffLine("-removed line"); !strings.Contains(got, ansiRed) {
		t.Fatalf("expected removed line to be red, got %q", got)
	}
	if got := r.formatDiffLine("@@ -1,1 +1,1 @@"); !strings.Contains(got, ansiCyan) {
		t.Fatalf("expected hunk line to be cyan, got %q", got)
	}
}
