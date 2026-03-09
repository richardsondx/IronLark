package render

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/richardsondx/IronLark/internal/core"
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

func TestResponseUsesStyledLabelsWhenColorEnabled(t *testing.T) {
	var out bytes.Buffer
	r := &Renderer{
		In:    bufio.NewReader(strings.NewReader("")),
		Out:   &out,
		Color: true,
	}

	err := r.Response(core.LLMResponse{
		Summary:  "Inspect the service definition",
		Findings: []string{"ExecStart exited with status=203/EXEC"},
	})
	if err != nil {
		t.Fatalf("response returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, ansiBold+"Summary"+ansiReset) {
		t.Fatalf("expected styled summary label, got %q", got)
	}
	if !strings.Contains(got, ansiBold+ansiCyan+"Findings"+ansiReset) {
		t.Fatalf("expected styled findings heading, got %q", got)
	}
	if !strings.Contains(got, ansiCyan+"1."+ansiReset) {
		t.Fatalf("expected styled findings index, got %q", got)
	}
}

func TestPlannedActionsStylesRiskAndActionTags(t *testing.T) {
	var out bytes.Buffer
	r := &Renderer{
		In:    bufio.NewReader(strings.NewReader("")),
		Out:   &out,
		Color: true,
	}

	r.PlannedActions([]core.Action{
		{
			Type:    core.ActionRunShell,
			Command: "journalctl -u openclaw -n 200 --no-pager",
			Reason:  "Get detailed failure logs",
		},
	}, []core.RiskReport{
		{
			Level:              core.RiskMedium,
			NeedsSudo:          false,
			TouchesSystemFiles: false,
			RollbackAvailable:  false,
		},
	})

	got := out.String()
	if !strings.Contains(got, ansiBold+ansiBlue+"[run_shell]"+ansiReset) {
		t.Fatalf("expected styled action tag, got %q", got)
	}
	if !strings.Contains(got, ansiBold+"Risk"+ansiReset+": "+ansiBold+ansiYellow+"MEDIUM"+ansiReset) {
		t.Fatalf("expected styled risk level, got %q", got)
	}
	if !strings.Contains(got, ansiBold+"Why"+ansiReset+": Get detailed failure logs") {
		t.Fatalf("expected styled why label, got %q", got)
	}
}

func TestResultSeparatesSummaryFromCapturedOutput(t *testing.T) {
	var out bytes.Buffer
	r := &Renderer{
		In:    bufio.NewReader(strings.NewReader("")),
		Out:   &out,
		Color: true,
	}

	r.Result(core.ActionResult{
		Action:  core.Action{Title: "Fetch recent journal logs for openclaw"},
		Summary: "Collected recent logs from journalctl",
		Stdout:  "line one\nline two",
		Stderr:  "warning line",
	})

	got := out.String()
	if !strings.Contains(got, ansiBold+"Summary"+ansiReset+": Collected recent logs from journalctl") {
		t.Fatalf("expected labeled summary, got %q", got)
	}
	if !strings.Contains(got, ansiBold+"Output"+ansiReset+":") {
		t.Fatalf("expected output section label, got %q", got)
	}
	if !strings.Contains(got, "  "+ansiBold+ansiGray+"│"+ansiReset+" "+ansiGray+"line one"+ansiReset) {
		t.Fatalf("expected prefixed stdout line, got %q", got)
	}
	if !strings.Contains(got, ansiBold+"Stderr"+ansiReset+":") {
		t.Fatalf("expected stderr section label, got %q", got)
	}
	if !strings.Contains(got, "  "+ansiBold+ansiRed+"│"+ansiReset+" "+ansiRed+"warning line"+ansiReset) {
		t.Fatalf("expected prefixed stderr line, got %q", got)
	}
}

func TestForceColorEnabled(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")

	if !forceColorEnabled() {
		t.Fatal("expected force color to be enabled")
	}
}

func TestShouldUseColorHonorsAlwaysMode(t *testing.T) {
	if !shouldUseColor(&bytes.Buffer{}, false, "always") {
		t.Fatal("expected always mode to force color")
	}
}

func TestShouldUseColorForceOverridesNoColor(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "1")

	if !shouldUseColor(&bytes.Buffer{}, false, "auto") {
		t.Fatal("expected FORCE_COLOR to override NO_COLOR")
	}
}

func TestShouldUseAgentColorDefaultsToTrue(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "")

	if !shouldUseAgentColor(&bytes.Buffer{}, "auto") {
		t.Fatal("expected agent color to default to true")
	}
}

func TestShouldUseAgentColorIgnoresNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	if !shouldUseAgentColor(&bytes.Buffer{}, "auto") {
		t.Fatal("expected agent color to stay enabled in TUI mode")
	}
}

func TestCollectUserInputTreatsEmptyEnterAsSkip(t *testing.T) {
	var out bytes.Buffer
	r := &Renderer{
		In:  bufio.NewReader(strings.NewReader("\n")),
		Out: &out,
	}

	result, err := r.CollectUserInput(core.Action{
		Type:         core.ActionAskUser,
		InputKind:    core.InputText,
		FieldKey:     "token",
		Prompt:       "Paste the token",
		Alternatives: []string{"submit", "skip", "follow_up"},
	})
	if err != nil {
		t.Fatalf("CollectUserInput() error = %v", err)
	}
	if result.ResponseMode != core.InputResponseSkipped {
		t.Fatalf("expected skip response, got %q", result.ResponseMode)
	}
}

func TestCollectUserInputSupportsFollowUpShortcut(t *testing.T) {
	var out bytes.Buffer
	r := &Renderer{
		In:  bufio.NewReader(strings.NewReader("/\nI need the docs URL first\n")),
		Out: &out,
	}

	result, err := r.CollectUserInput(core.Action{
		Type:         core.ActionAskUser,
		InputKind:    core.InputText,
		FieldKey:     "token",
		Prompt:       "Paste the token",
		Alternatives: []string{"submit", "skip", "follow_up"},
	})
	if err != nil {
		t.Fatalf("CollectUserInput() error = %v", err)
	}
	if result.ResponseMode != core.InputResponseFollowUp {
		t.Fatalf("expected follow-up response, got %q", result.ResponseMode)
	}
	if result.InputValue != "I need the docs URL first" {
		t.Fatalf("unexpected follow-up value %q", result.InputValue)
	}
}

func TestCollectUserInputSecretDoesNotRedactBufferedValue(t *testing.T) {
	var out bytes.Buffer
	r := &Renderer{
		In:  bufio.NewReader(strings.NewReader("super-secret\n")),
		Out: &out,
	}

	result, err := r.CollectUserInput(core.Action{
		Type:         core.ActionAskUser,
		InputKind:    core.InputSecret,
		FieldKey:     "api_key",
		Prompt:       "Paste the API key",
		Alternatives: []string{"submit", "skip", "follow_up"},
	})
	if err != nil {
		t.Fatalf("CollectUserInput() error = %v", err)
	}
	if result.InputValue != "super-secret" {
		t.Fatalf("unexpected secret value %q", result.InputValue)
	}
	if strings.Contains(out.String(), "[secret]") {
		t.Fatalf("expected no secret redaction marker, got %q", out.String())
	}
}
