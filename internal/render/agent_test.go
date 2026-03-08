package render

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/policy"
)

func TestAgentHeaderUsesCompactModeForShortTerminals(t *testing.T) {
	renderer := newTestAgentRenderer()

	compact := renderer.headerLines(80, 24)
	full := renderer.headerLines(80, 40)

	if len(compact) != 1 {
		t.Fatalf("expected compact header with 1 line, got %d", len(compact))
	}
	if len(full) != 2 {
		t.Fatalf("expected full header with 2 lines, got %d", len(full))
	}
}

func TestUpDownScrollTranscriptWhenNoOverlayIsOpen(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	for i := 0; i < 40; i++ {
		renderer.transcript = append(renderer.transcript, "line")
	}

	if submitted, done := renderer.handleKey(keyPress{Kind: keyUp}); submitted != "" || done {
		t.Fatalf("expected scroll only, got %q %t", submitted, done)
	}
	if renderer.overlay != overlayNone {
		t.Fatalf("expected no overlay, got %q", renderer.overlay)
	}
	if renderer.scrollOffset != 1 {
		t.Fatalf("expected scroll offset 1, got %d", renderer.scrollOffset)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyDown}); submitted != "" || done {
		t.Fatalf("expected scroll only, got %q %t", submitted, done)
	}
	if renderer.scrollOffset != 0 {
		t.Fatalf("expected scroll offset reset to 0, got %d", renderer.scrollOffset)
	}
}

func TestDrawLockedLimitsTranscriptToViewportHeightWhenScrolled(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.mu.Lock()
	renderer.Out = &bytes.Buffer{}
	renderer.sizeFn = func() (int, int) { return 80, 12 }
	renderer.transcript = nil
	for i := 0; i < 20; i++ {
		renderer.transcript = append(renderer.transcript, "line")
	}
	renderer.scrollOffset = 1

	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("drawLocked() error = %v", err)
	}
	output := renderer.Out.(*bytes.Buffer).String()
	renderer.mu.Unlock()

	if count := strings.Count(output, "line\033[K"); count != 8 {
		t.Fatalf("expected 8 visible transcript lines, got %d in output %q", count, output)
	}
}

func TestDrawLockedKeepsCursorOnPromptLine(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.mu.Lock()
	renderer.Out = &bytes.Buffer{}

	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("drawLocked() error = %v", err)
	}
	output := renderer.Out.(*bytes.Buffer).String()
	renderer.mu.Unlock()

	if !strings.Contains(output, "> \033[K") {
		t.Fatalf("expected output to contain prompt line before footer, got %q", output)
	}
	if !strings.HasSuffix(output, "\033[24;1H") {
		t.Fatalf("expected output to end by positioning the cursor on the last row, got %q", output)
	}
	if !strings.Contains(output, "IronLark Agent") {
		t.Fatalf("expected output to end on footer line, got %q", output)
	}
}

func TestDrawLockedAvoidsFullClearOnIncrementalRedraw(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.mu.Lock()
	renderer.Out = &bytes.Buffer{}
	renderer.composer = []rune("hello")
	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("first drawLocked() error = %v", err)
	}
	first := renderer.Out.(*bytes.Buffer).String()
	renderer.Out = &bytes.Buffer{}
	renderer.composer = []rune("hello!")
	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("second drawLocked() error = %v", err)
	}
	second := renderer.Out.(*bytes.Buffer).String()
	renderer.mu.Unlock()

	if !strings.Contains(first, "\033[H\033[2J") {
		t.Fatalf("expected initial full clear, got %q", first)
	}
	if strings.Contains(second, "\033[H\033[2J") {
		t.Fatalf("expected incremental redraw without full clear, got %q", second)
	}
	if !strings.Contains(second, "hello!") {
		t.Fatalf("expected updated prompt content in incremental redraw, got %q", second)
	}
}

func TestFooterLineUsesGreenBarWithAgentLabel(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.Color = true
	line := renderer.footerLineLocked(48)

	if !strings.Contains(line, ansiBgGreen) {
		t.Fatalf("expected green background in footer, got %q", line)
	}
	if !strings.Contains(line, "IronLark Agent") {
		t.Fatalf("expected agent label in footer, got %q", line)
	}
	if !strings.Contains(line, "\"srv-1\"  thread-1") {
		t.Fatalf("expected host and thread id in footer, got %q", line)
	}
	if visibleWidth(line) != 48 {
		t.Fatalf("expected padded footer width 48, got %d", visibleWidth(line))
	}
}

func TestSlashMenuShowsModeToggleAndSelectsHelp(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: '/'})

	if renderer.overlay != overlaySlash {
		t.Fatalf("expected slash overlay, got %q", renderer.overlay)
	}
	commands := renderer.filteredSlashCommandsLockedForTest()
	if len(commands) == 0 || commands[0].Label != "mode: execute-first" {
		t.Fatalf("unexpected filtered commands %#v", commands)
	}
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'h'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'e'})
	commands = renderer.filteredSlashCommandsLockedForTest()
	if len(commands) != 1 || commands[0].Execute != "/help" {
		t.Fatalf("unexpected filtered commands %#v", commands)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); !done || submitted != "/help" {
		t.Fatalf("expected /help submission, got %q %t", submitted, done)
	}
}

func TestSlashMenuModeToggleSubmitsOppositeMode(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: '/'})

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); !done || submitted != "/mode plan-first" {
		t.Fatalf("expected mode toggle submission, got %q %t", submitted, done)
	}
}

func TestSlashMenuApprovalOpensApprovalOverlay(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: '/'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'a'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'p'})

	commands := renderer.filteredSlashCommandsLockedForTest()
	if len(commands) == 0 || commands[0].Execute != "/approval" {
		t.Fatalf("unexpected filtered commands %#v", commands)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || done {
		t.Fatalf("expected approval overlay open, got %q %t", submitted, done)
	}
	if renderer.overlay != overlayApproval {
		t.Fatalf("expected approval overlay, got %q", renderer.overlay)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyDown}); submitted != "" || done {
		t.Fatalf("expected selection move, got %q %t", submitted, done)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); !done || submitted != "/approval auto-safe" {
		t.Fatalf("expected approval command, got %q %t", submitted, done)
	}
}

func TestSlashMenuSecretToggleSubmitsHiddenByDefault(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: '/'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 's'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'e'})

	commands := renderer.filteredSlashCommandsLockedForTest()
	if len(commands) == 0 || commands[0].Execute != "/secret hidden" {
		t.Fatalf("unexpected filtered commands %#v", commands)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); !done || submitted != "/secret hidden" {
		t.Fatalf("expected secret toggle command, got %q %t", submitted, done)
	}
}

func TestPolicyOverlayTogglesRuleDecision(t *testing.T) {
	tmp := t.TempDir()
	store := policy.Store{Path: filepath.Join(tmp, "policy.json")}
	rule, err := store.Add(policy.Rule{
		Decision: policy.DecisionAllow,
		Kind:     policy.RuleActionType,
		Value:    "run_shell",
		Action:   string(core.ActionRunShell),
		Scope:    "machine",
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	renderer := newTestAgentRenderer()
	renderer.meta.PolicyStore = store
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.openPolicyOverlayLocked()
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || done {
		t.Fatalf("expected in-place toggle, got %q %t", submitted, done)
	}
	rules, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(rules) != 1 || rules[0].ID == rule.ID || rules[0].Decision != policy.DecisionDeny {
		t.Fatalf("expected toggled deny rule, got %#v", rules)
	}
}

func TestSetInteractionUpdatesHeaderStatus(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.SetInteraction(core.InteractionPlanFirst)

	header := renderer.headerLines(120, 40)
	if header[0] == "" || !containsString(header[0], "mode=plan-first") {
		t.Fatalf("expected plan-first in header, got %q", header[0])
	}
	if containsString(strings.Join(header, " "), "host=") || containsString(strings.Join(header, " "), "thread=") || containsString(strings.Join(header, " "), "model=") {
		t.Fatalf("expected compact header metadata to be removed, got %#v", header)
	}
}

func TestThinkingIndicatorAnimatesAndStopsCleanly(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.redrawInterval = 5 * time.Millisecond
	renderer.BeginThinking("Thinking...")
	time.Sleep(20 * time.Millisecond)

	renderer.mu.Lock()
	frame := renderer.thinkingFrame
	status := renderer.statusLineLocked(80)
	renderer.mu.Unlock()

	if frame == 0 {
		t.Fatalf("expected thinking frame to advance")
	}
	if !containsString(status, "Thinking...") || !containsString(status, "Ctrl+C to stop") {
		t.Fatalf("expected thinking label in status line, got %q", status)
	}

	renderer.EndThinking()
	renderer.mu.Lock()
	stillThinking := renderer.thinking
	idleStatus := renderer.statusLineLocked(80)
	renderer.mu.Unlock()
	if stillThinking {
		t.Fatal("expected thinking state to clear")
	}
	if containsString(idleStatus, "Thinking...") {
		t.Fatalf("expected idle status line, got %q", idleStatus)
	}
}

func TestAgentResultUsesColoredOutputBlocks(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.Color = true
	renderer.Result(core.ActionResult{
		Action:  core.Action{Title: "Show man output"},
		Summary: "Collected terminal output",
		Stdout:  "NAME\nls - list directory contents",
		Stderr:  "warning line",
	})

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	joined := strings.Join(renderer.transcript, "\n")
	if !containsString(joined, ansiBold+"Output"+ansiReset+":") {
		t.Fatalf("expected colored output label, got %q", joined)
	}
	if !containsString(joined, "  "+ansiBold+ansiGray+"│"+ansiReset+" "+ansiGray+"NAME"+ansiReset) {
		t.Fatalf("expected colored stdout prefix, got %q", joined)
	}
	if !containsString(joined, "  "+ansiBold+ansiRed+"│"+ansiReset+" "+ansiRed+"warning line"+ansiReset) {
		t.Fatalf("expected colored stderr prefix, got %q", joined)
	}
}

func TestMessageSplitsMultilineText(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.Message("line one\nline two")

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	foundOne := false
	foundTwo := false
	for _, line := range renderer.transcript {
		if line == "line one" {
			foundOne = true
		}
		if line == "line two" {
			foundTwo = true
		}
	}
	if !foundOne || !foundTwo {
		t.Fatalf("expected split lines in transcript, got %#v", renderer.transcript)
	}
}

func TestClearScreenResetsTranscriptAndComposer(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.Message("hello")
	renderer.mu.Lock()
	renderer.composer = []rune("/help")
	renderer.overlay = overlaySlash
	renderer.mu.Unlock()

	renderer.ClearScreen()

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	if len(renderer.transcript) != 0 {
		t.Fatalf("expected empty transcript, got %#v", renderer.transcript)
	}
	if len(renderer.composer) != 0 {
		t.Fatalf("expected empty composer, got %q", string(renderer.composer))
	}
	if renderer.overlay != overlayNone {
		t.Fatalf("expected no overlay, got %q", renderer.overlay)
	}
}

func TestTruncateUsesASCIIEllipsis(t *testing.T) {
	if got := truncate("abcdefghijklmnopqrstuvwxyz", 10); got != "abcdefg..." {
		t.Fatalf("unexpected truncate result %q", got)
	}
}

func TestBlockerPanelTabCyclesFocusAndSkipOnEmptyEnter(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.overlay = overlayBlocker
	renderer.blocker = &blockerState{
		action: core.Action{
			Type:         core.ActionAskUser,
			InputKind:    core.InputText,
			FieldKey:     "token",
			Prompt:       "Paste the token",
			Alternatives: []string{"submit", "skip", "follow_up"},
		},
		focus: blockerFocusInput,
	}
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyTab}); submitted != "" || done {
		t.Fatalf("expected focus move only, got %q %t", submitted, done)
	}
	renderer.mu.Lock()
	if renderer.blocker.focus != blockerFocusSkip {
		t.Fatalf("expected skip focus, got %v", renderer.blocker.focus)
	}
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || !done {
		t.Fatalf("expected skip submit, got %q %t", submitted, done)
	}
}

func TestBlockerPanelFollowUpCapturesClarification(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.overlay = overlayBlocker
	renderer.blocker = &blockerState{
		action: core.Action{
			Type:         core.ActionAskUser,
			InputKind:    core.InputText,
			FieldKey:     "token",
			Prompt:       "Paste the token",
			Alternatives: []string{"submit", "skip", "follow_up"},
		},
		focus: blockerFocusFollowUp,
	}
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || done {
		t.Fatalf("expected follow-up edit mode, got %q %t", submitted, done)
	}
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'h'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'i'})
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "hi" || !done {
		t.Fatalf("expected follow-up submission, got %q %t", submitted, done)
	}
}

func TestBlockerPanelShowsSecretInputByDefault(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.overlay = overlayBlocker
	renderer.blocker = &blockerState{
		action: core.Action{
			Type:         core.ActionAskUser,
			InputKind:    core.InputSecret,
			FieldKey:     "api_key",
			Prompt:       "Paste the API key",
			Alternatives: []string{"submit", "skip", "follow_up"},
		},
		focus: blockerFocusInput,
	}
	renderer.blocker.answer = []rune("secret")
	line := renderer.promptLineLocked(80)
	renderer.mu.Unlock()
	if !containsString(line, "secret") {
		t.Fatalf("expected visible secret prompt line, got %q", line)
	}
}

func TestBlockerPanelMasksSecretInputWhenHidden(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.SetSecretVisibility(false)
	renderer.mu.Lock()
	renderer.overlay = overlayBlocker
	renderer.blocker = &blockerState{
		action: core.Action{
			Type:         core.ActionAskUser,
			InputKind:    core.InputSecret,
			FieldKey:     "api_key",
			Prompt:       "Paste the API key",
			Alternatives: []string{"submit", "skip", "follow_up"},
		},
		focus: blockerFocusInput,
	}
	renderer.blocker.answer = []rune("secret")
	line := renderer.promptLineLocked(80)
	renderer.mu.Unlock()
	if containsString(line, "secret") {
		t.Fatalf("expected masked secret prompt line, got %q", line)
	}
	if !containsString(line, "******") {
		t.Fatalf("expected masked placeholder, got %q", line)
	}
}

func TestReadKeyDecodesBracketedPasteAsText(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.In = bufio.NewReader(strings.NewReader("\x1b[200~hello\nworld\x1b[201~"))

	key, err := renderer.readKey()
	if err != nil {
		t.Fatalf("readKey() error = %v", err)
	}
	if key.Kind != keyPaste {
		t.Fatalf("expected paste key, got %v", key.Kind)
	}
	if key.Text != "hello\nworld" {
		t.Fatalf("expected pasted text, got %q", key.Text)
	}
}

func TestBracketedPasteStaysInComposerUntilExplicitSubmit(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyPaste, Text: "first line\nsecond line"}); submitted != "" || done {
		t.Fatalf("expected paste to edit only, got %q %t", submitted, done)
	}

	renderer.mu.Lock()
	composer := string(renderer.composer)
	promptLine := renderer.promptLineLocked(80)
	renderer.mu.Unlock()

	if composer != "first line\nsecond line" {
		t.Fatalf("expected multiline composer, got %q", composer)
	}
	if !containsString(promptLine, "first line\\nsecond line") {
		t.Fatalf("expected escaped newline preview, got %q", promptLine)
	}

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); !done || submitted != "first line\nsecond line" {
		t.Fatalf("expected explicit submit after paste, got %q %t", submitted, done)
	}
}

func TestBlockerPanelAcceptsBracketedPasteWithoutSubmitting(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.overlay = overlayBlocker
	renderer.blocker = &blockerState{
		action: core.Action{
			Type:         core.ActionAskUser,
			InputKind:    core.InputText,
			FieldKey:     "notes",
			Prompt:       "Paste the notes",
			Alternatives: []string{"submit", "skip", "follow_up"},
		},
		focus: blockerFocusInput,
	}
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyPaste, Text: "alpha\nbeta"}); submitted != "" || done {
		t.Fatalf("expected blocker paste to edit only, got %q %t", submitted, done)
	}

	renderer.mu.Lock()
	value := string(renderer.blocker.answer)
	promptLine := renderer.promptLineLocked(80)
	renderer.mu.Unlock()

	if value != "alpha\nbeta" {
		t.Fatalf("expected multiline blocker value, got %q", value)
	}
	if !containsString(promptLine, "alpha\\nbeta") {
		t.Fatalf("expected escaped newline preview, got %q", promptLine)
	}

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); !done || submitted != "alpha\nbeta" {
		t.Fatalf("expected explicit blocker submit after paste, got %q %t", submitted, done)
	}
}

func newTestAgentRenderer() *AgentRenderer {
	renderer := NewAgent(nil, discardWriter{}, discardWriter{}, "never", AgentMeta{
		Host:          "srv-1",
		CWD:           "/opt/app",
		Model:         "gpt-5-mini",
		ApprovalMode:  "confirm",
		ThreadID:      "thread-1",
		CompactAtRows: 28,
		Interaction:   core.InteractionExecuteFirst,
	})
	renderer.sizeFn = func() (int, int) { return 80, 24 }
	return renderer
}

func (r *AgentRenderer) filteredSlashCommandsLockedForTest() []slashCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]slashCommand(nil), r.filteredSlashCommandsLocked()...)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func containsString(value, needle string) bool {
	return len(value) >= len(needle) && (value == needle || containsAtAnyIndex(value, needle))
}

func containsAtAnyIndex(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
