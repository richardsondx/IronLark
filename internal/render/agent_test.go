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
	if len(full) != 1 {
		t.Fatalf("expected full header with 1 line, got %d", len(full))
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

func TestTabOpensHistoryOverlayAndRecallsPrompt(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.promptHistory = []string{"first prompt", "second prompt"}

	if submitted, done := renderer.handleKey(keyPress{Kind: keyTab}); submitted != "" || done {
		t.Fatalf("expected history overlay only, got %q %t", submitted, done)
	}
	if renderer.overlay != overlayHistory {
		t.Fatalf("expected history overlay, got %q", renderer.overlay)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyDown}); submitted != "" || done {
		t.Fatalf("expected history selection move only, got %q %t", submitted, done)
	}
	if renderer.overlayIndex != 1 {
		t.Fatalf("expected second history item selected, got %d", renderer.overlayIndex)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || done {
		t.Fatalf("expected recall into composer only, got %q %t", submitted, done)
	}
	if renderer.overlay != overlayNone {
		t.Fatalf("expected overlay to close, got %q", renderer.overlay)
	}
	if got := string(renderer.composer); got != "first prompt" {
		t.Fatalf("expected selected prompt in composer, got %q", got)
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

	if count := strings.Count(output, "line\033[K"); count != 7 {
		t.Fatalf("expected 7 visible transcript lines, got %d in output %q", count, output)
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
	if !strings.Contains(output, "--------------------------------------------------------------------------------\033[K") {
		t.Fatalf("expected output to contain input border line, got %q", output)
	}
	if !strings.HasSuffix(output, "\033[22;3H\033[?25h") {
		t.Fatalf("expected output to end by positioning the cursor on the prompt, got %q", output)
	}
	if !strings.Contains(output, "IronLark Agent") {
		t.Fatalf("expected output to end on footer line, got %q", output)
	}
}

func TestDrawLockedPlacesCursorAtWrappedPromptTail(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.mu.Lock()
	renderer.Out = &bytes.Buffer{}
	renderer.sizeFn = func() (int, int) { return 12, 24 }
	renderer.composer = []rune("abcdefghijk")
	renderer.cursor = len(renderer.composer)

	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("drawLocked() error = %v", err)
	}
	output := renderer.Out.(*bytes.Buffer).String()
	renderer.mu.Unlock()

	if !strings.HasSuffix(output, "\033[22;13H\033[?25h") {
		t.Fatalf("expected wrapped prompt cursor at tail, got %q", output)
	}
}

func TestDrawLockedFreshSessionShowsWelcomeTitle(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.meta.WelcomeBack = false

	renderer.mu.Lock()
	renderer.Out = &bytes.Buffer{}
	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("drawLocked() error = %v", err)
	}
	output := renderer.Out.(*bytes.Buffer).String()
	renderer.mu.Unlock()

	if !strings.Contains(output, "Welcome!") {
		t.Fatalf("expected first-launch welcome title, got %q", output)
	}
}

func TestDrawLockedReturningSessionShowsWelcomeBackTitle(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.meta.WelcomeBack = true
	renderer.sizeFn = func() (int, int) { return 120, 24 }

	renderer.mu.Lock()
	renderer.Out = &bytes.Buffer{}
	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("drawLocked() error = %v", err)
	}
	output := renderer.Out.(*bytes.Buffer).String()
	renderer.mu.Unlock()

	if !strings.Contains(output, "Welcome back!") {
		t.Fatalf("expected returning-user welcome title, got %q", output)
	}
	if !strings.Contains(output, "┌") || !strings.Contains(output, "│") {
		t.Fatalf("expected welcome box framing, got %q", output)
	}
	if !strings.Contains(output, "IronLark v") {
		t.Fatalf("expected titled frame, got %q", output)
	}
	if !strings.Contains(output, "gpt-4.1-mini") {
		t.Fatalf("expected model line, got %q", output)
	}
	if !strings.Contains(output, "/opt/app") {
		t.Fatalf("expected compact cwd line, got %q", output)
	}
}

func TestDrawLockedBottomAlignsTranscriptNearInput(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.Message("latest line")

	renderer.mu.Lock()
	renderer.Out = &bytes.Buffer{}
	renderer.sizeFn = func() (int, int) { return 80, 12 }
	if err := renderer.drawLocked(); err != nil {
		renderer.mu.Unlock()
		t.Fatalf("drawLocked() error = %v", err)
	}
	output := renderer.Out.(*bytes.Buffer).String()
	renderer.mu.Unlock()

	if !strings.Contains(output, "\033[8;1Hlatest line") {
		t.Fatalf("expected transcript close to input area, got %q", output)
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
	line := renderer.footerLineLocked(96)

	if !strings.Contains(line, ansiBgGreen) {
		t.Fatalf("expected green background in footer, got %q", line)
	}
	if !strings.Contains(line, "IronLark Agent") {
		t.Fatalf("expected agent label in footer, got %q", line)
	}
	if !strings.Contains(line, "approval=confirm  mode=execute-first") {
		t.Fatalf("expected approval and mode in footer, got %q", line)
	}
	if !strings.Contains(line, "\"srv-1\"  thread-1") {
		t.Fatalf("expected host and thread id in footer, got %q", line)
	}
	if visibleWidth(line) != 96 {
		t.Fatalf("expected padded footer width 96, got %d", visibleWidth(line))
	}
}

func TestAgentResponseUsesConversationalAssistantLayout(t *testing.T) {
	renderer := newTestAgentRenderer()

	if err := renderer.Response(core.LLMResponse{
		Summary:  "IronLark is a Go-based SSH-first AI terminal assistant.",
		Findings: []string{"Build and install are managed through the Makefile."},
	}); err != nil {
		t.Fatalf("Response() error = %v", err)
	}

	transcript := strings.Join(renderer.transcript, "\n")
	if !strings.Contains(transcript, "⏹ IronLark is a Go-based SSH-first AI terminal assistant.") {
		t.Fatalf("expected assistant marker prefix, got %q", transcript)
	}
	if strings.Contains(transcript, "## Lark") || strings.Contains(transcript, "Findings:") || strings.Contains(transcript, "Summary:") {
		t.Fatalf("expected conversational response layout, got %q", transcript)
	}
	if !strings.Contains(transcript, "- Build and install are managed through the Makefile.") {
		t.Fatalf("expected findings folded into response details, got %q", transcript)
	}
}

func TestAgentResponseDropsRepetitiveFindings(t *testing.T) {
	renderer := newTestAgentRenderer()

	if err := renderer.Response(core.LLMResponse{
		Summary: "IronLark is a Go-based SSH-first AI terminal assistant.",
		Findings: []string{
			"IronLark is a Go-based SSH-first AI terminal assistant.",
			"The project is a Go-based SSH-first AI terminal assistant",
			"Build and install are managed through the Makefile.",
			"Build and install are managed through the Makefile.",
		},
	}); err != nil {
		t.Fatalf("Response() error = %v", err)
	}

	transcript := strings.Join(renderer.transcript, "\n")
	if strings.Contains(transcript, "- IronLark is a Go-based SSH-first AI terminal assistant.") {
		t.Fatalf("expected summary-duplicate findings to be dropped, got %q", transcript)
	}
	if count := strings.Count(transcript, "- Build and install are managed through the Makefile."); count != 1 {
		t.Fatalf("expected distinct finding once, got %d in %q", count, transcript)
	}
}

func TestWrapWithWidthWrapsAnsiStyledLines(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.Color = true

	line := renderer.assistantPrefix() + "The project setup uses a Go binary, a Makefile, and a local config directory."
	wrapped := renderer.wrapWithWidth(line, 30)

	if len(wrapped) < 2 {
		t.Fatalf("expected styled line to wrap, got %#v", wrapped)
	}
	for _, part := range wrapped {
		if visibleWidth(part) > 30 {
			t.Fatalf("expected wrapped segment width <= 30, got %d for %q", visibleWidth(part), part)
		}
	}
}

func TestUserEntryUsesHighlightedChipLayout(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.Color = true
	renderer.writeUserInput("hi")

	transcript := strings.Join(renderer.transcript, "\n")
	if !strings.Contains(transcript, "› hi ") {
		t.Fatalf("expected highlighted user chip, got %q", transcript)
	}
	if !strings.Contains(transcript, ansiBgWhite) {
		t.Fatalf("expected user chip highlight style, got %q", transcript)
	}
}

func TestVisibleWidthCountsUnicodeRunes(t *testing.T) {
	if got := visibleWidth("┌──⏹──┐"); got != 7 {
		t.Fatalf("expected rune-aware width, got %d", got)
	}
}

func TestRenderPromptValueStripsControlAndEscapeSequences(t *testing.T) {
	value := "abc\x12\x1b[A\x1b[Bdef"
	got := renderPromptValue(value)

	if got != "abcdef" {
		t.Fatalf("expected sanitized prompt value, got %q", got)
	}
}

func TestRenderPromptValueStripsCaretControlLeakNotation(t *testing.T) {
	value := "abc^P^N^R^[[A^[[Bdef"
	got := renderPromptValue(value)

	if got != "abcdef" {
		t.Fatalf("expected leaked caret notation removed, got %q", got)
	}
}

func TestInsertTextLockedStripsControlAndEscapeSequences(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.mu.Lock()
	renderer.insertTextLocked("abc\x12\x1b[A\x1b[Bdef")
	renderer.mu.Unlock()

	if string(renderer.composer) != "abcdef" {
		t.Fatalf("expected sanitized composer, got %q", string(renderer.composer))
	}
}

func TestInsertTextLockedStripsCaretControlLeakNotation(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.mu.Lock()
	renderer.insertTextLocked("abc^P^N^R^[[A^[[Bdef")
	renderer.mu.Unlock()

	if string(renderer.composer) != "abcdef" {
		t.Fatalf("expected leaked caret notation removed from composer, got %q", string(renderer.composer))
	}
}

func TestInsertRuneLockedStripsIncrementalCaretControlLeakNotation(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.mu.Lock()
	for _, ch := range "abc^P^N^R^[[A^[[Bdef" {
		renderer.insertRuneLocked(ch)
	}
	renderer.mu.Unlock()

	if string(renderer.composer) != "abcdef" {
		t.Fatalf("expected incremental leaked caret notation removed from composer, got %q", string(renderer.composer))
	}
}

func TestHandleBlockerKeyLockedStripsIncrementalCaretControlLeakNotation(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.mu.Lock()
	renderer.blocker = &blockerState{focus: blockerFocusInput}
	for _, ch := range "abc^P^N^R^[[A^[[Bdef" {
		renderer.handleBlockerKeyLocked(keyPress{Kind: keyPrintable, Rune: ch})
	}
	got := string(renderer.blocker.answer)
	renderer.mu.Unlock()

	if got != "abcdef" {
		t.Fatalf("expected incremental leaked caret notation removed from blocker answer, got %q", got)
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

func TestApprovalDecisionTabCyclesAutoAcceptLevel(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.overlay = overlayApprovalDecision
	renderer.overlayIndex = 2
	renderer.approvalDecision = &approvalDecisionState{selected: 2, level: core.RiskMedium}
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyTab}); submitted != "" || done {
		t.Fatalf("expected selection update only, got %q %t", submitted, done)
	}

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	if renderer.approvalDecision.level != core.RiskHigh {
		t.Fatalf("expected auto accept level to advance to high, got %q", renderer.approvalDecision.level)
	}
}

func TestApprovalDecisionEnterReturnsStructuredDecision(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.overlay = overlayApprovalDecision
	renderer.overlayIndex = 2
	renderer.approvalDecision = &approvalDecisionState{selected: 2, level: core.RiskMedium}
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || !done {
		t.Fatalf("expected completion only, got %q %t", submitted, done)
	}

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	if renderer.approvalDecision.result.Kind != core.ApprovalDecisionAutoAccept || renderer.approvalDecision.result.AutoAcceptThrough != core.RiskMedium {
		t.Fatalf("unexpected approval decision %#v", renderer.approvalDecision.result)
	}
}

func TestSlashMenuModelOpensModelOverlay(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()

	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: '/'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'm'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'o'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'd'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'e'})
	renderer.handleKey(keyPress{Kind: keyPrintable, Rune: 'l'})

	commands := renderer.filteredSlashCommandsLockedForTest()
	foundModel := false
	for _, command := range commands {
		if command.Execute == "/model" {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Fatalf("unexpected filtered commands %#v", commands)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || done {
		t.Fatalf("expected model overlay open, got %q %t", submitted, done)
	}
	if renderer.overlay != overlayModel {
		t.Fatalf("expected model overlay, got %q", renderer.overlay)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyUp}); submitted != "" || done {
		t.Fatalf("expected selection move, got %q %t", submitted, done)
	}
	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); !done || submitted != "/model gpt-5-mini" {
		t.Fatalf("expected model command, got %q %t", submitted, done)
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

	if submitted, done := renderer.handleKey(keyPress{Kind: keyDown}); submitted != "" || done {
		t.Fatalf("expected selection move, got %q %t", submitted, done)
	}

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

func TestPolicyOverlayShowsAutoAcceptThreshold(t *testing.T) {
	tmp := t.TempDir()
	store := policy.Store{Path: filepath.Join(tmp, "policy.json")}
	if err := store.SetAutoAcceptThrough(core.RiskMedium); err != nil {
		t.Fatalf("SetAutoAcceptThrough() error = %v", err)
	}

	renderer := newTestAgentRenderer()
	renderer.meta.PolicyStore = store
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.openPolicyOverlayLocked()
	lines := renderer.overlayLinesLocked(80)
	renderer.mu.Unlock()

	joined := strings.Join(lines, "\n")
	if !containsString(joined, "auto-accept <= MEDIUM") {
		t.Fatalf("expected auto-accept threshold in policy overlay, got %q", joined)
	}
	if !containsString(joined, "no machine rules yet") {
		t.Fatalf("expected empty rules message in policy overlay, got %q", joined)
	}
}

func TestPolicyOverlayEnterCyclesAutoAcceptThreshold(t *testing.T) {
	tmp := t.TempDir()
	store := policy.Store{Path: filepath.Join(tmp, "policy.json")}

	renderer := newTestAgentRenderer()
	renderer.meta.PolicyStore = store
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.openPolicyOverlayLocked()
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyEnter}); submitted != "" || done {
		t.Fatalf("expected in-place cycle, got %q %t", submitted, done)
	}
	level, ok, err := store.AutoAcceptThrough()
	if err != nil {
		t.Fatalf("AutoAcceptThrough() error = %v", err)
	}
	if !ok || level != core.RiskLow {
		t.Fatalf("expected auto-accept to cycle to low, got %q %t", level, ok)
	}
}

func TestPolicyOverlayTabCyclesAutoAcceptThreshold(t *testing.T) {
	tmp := t.TempDir()
	store := policy.Store{Path: filepath.Join(tmp, "policy.json")}
	if err := store.SetAutoAcceptThrough(core.RiskMedium); err != nil {
		t.Fatalf("SetAutoAcceptThrough() error = %v", err)
	}

	renderer := newTestAgentRenderer()
	renderer.meta.PolicyStore = store
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.openPolicyOverlayLocked()
	renderer.mu.Unlock()

	if submitted, done := renderer.handleKey(keyPress{Kind: keyTab}); submitted != "" || done {
		t.Fatalf("expected in-place tab cycle, got %q %t", submitted, done)
	}
	level, ok, err := store.AutoAcceptThrough()
	if err != nil {
		t.Fatalf("AutoAcceptThrough() error = %v", err)
	}
	if !ok || level != core.RiskHigh {
		t.Fatalf("expected auto-accept to cycle to high, got %q %t", level, ok)
	}
}

func TestSetInteractionUpdatesHeaderStatus(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.SetInteraction(core.InteractionPlanFirst)

	footer := renderer.footerLineLocked(120)
	if !containsString(footer, "mode=plan-first") {
		t.Fatalf("expected plan-first in footer, got %q", footer)
	}
	header := renderer.headerLines(120, 40)
	if containsString(strings.Join(header, " "), "approval=") || containsString(strings.Join(header, " "), "mode=") {
		t.Fatalf("expected mode metadata removed from header, got %#v", header)
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

func TestActionStatusAnimatesAndStopsCleanly(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.redrawInterval = 5 * time.Millisecond
	renderer.setActionStatus("Running action...")
	time.Sleep(20 * time.Millisecond)

	renderer.mu.Lock()
	frame := renderer.actionFrame
	status := renderer.statusLineLocked(80)
	renderer.mu.Unlock()

	if frame == 0 {
		t.Fatalf("expected action frame to advance")
	}
	if !containsString(status, "Running action...") {
		t.Fatalf("expected action label in status line, got %q", status)
	}

	renderer.setActionStatus("")
	renderer.mu.Lock()
	actionActive := renderer.actionStatus
	idleStatus := renderer.statusLineLocked(80)
	renderer.mu.Unlock()
	if actionActive != "" {
		t.Fatal("expected action status to clear")
	}
	if containsString(idleStatus, "Running action...") {
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

func TestNarratedProgressRendersTimelineEntries(t *testing.T) {
	renderer := newNarratedTestAgentRenderer()
	renderer.Narrate(core.NarrativeEvent{
		Kind:   core.NarrativeIntent,
		Phase:  "Reading files",
		Text:   "I found a likely starting point in README.md, so I'm checking that first.",
		Status: core.NarrativeDone,
	})
	renderer.ActionProgress(core.Action{
		ID:     "readme",
		Type:   core.ActionReadFiles,
		Path:   "README.md",
		Reason: "inspect current docs",
	})
	renderer.Result(core.ActionResult{
		Action:  core.Action{ID: "readme", Type: core.ActionReadFiles, Path: "README.md"},
		Summary: "read 1 file(s)",
		Stdout:  strings.Repeat("line\n", 8),
	})

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	joined := strings.Join(renderer.transcript, "\n")
	if !containsString(joined, "o I found a likely starting point") {
		t.Fatalf("expected narration line, got %q", joined)
	}
	if !containsString(joined, "Read(README.md)") {
		t.Fatalf("expected action timeline title, got %q", joined)
	}
	if !containsString(joined, "detail line(s) hidden") {
		t.Fatalf("expected collapsed detail summary, got %q", joined)
	}
}

func TestNarratedProgressUsesNarrativePhaseInStatusLine(t *testing.T) {
	renderer := newNarratedTestAgentRenderer()
	renderer.Narrate(core.NarrativeEvent{
		Kind:   core.NarrativeActionStarted,
		Phase:  "Reading files",
		Text:   "Let me read the current tests first.",
		Status: core.NarrativeRunning,
	})
	renderer.redrawInterval = 5 * time.Millisecond
	renderer.ActionProgress(core.Action{ID: "read-tests", Type: core.ActionReadFiles, Path: "tests.go"})
	time.Sleep(20 * time.Millisecond)

	renderer.mu.Lock()
	status := renderer.statusLineLocked(80)
	renderer.mu.Unlock()
	if !containsString(status, "Reading files") {
		t.Fatalf("expected phase label in status line, got %q", status)
	}
}

func TestWebSearchActionProgressShowsQueryAndSiteFilter(t *testing.T) {
	renderer := newNarratedTestAgentRenderer()
	renderer.meta.Provider = "openai"
	renderer.ActionProgress(core.Action{
		ID:    "search-docs",
		Type:  core.ActionWebSearch,
		Query: "site:harborframework.com/docs BaseInstalledAgent MyInstalledAgent job.yaml agents",
	})

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	joined := strings.Join(renderer.transcript, "\n")
	if !containsString(joined, "WebSearch(site:harborframework.com/docs") {
		t.Fatalf("expected web search action entry, got %q", joined)
	}
	if !containsString(joined, "provider: openai") {
		t.Fatalf("expected provider detail, got %q", joined)
	}
	if !containsString(joined, "site filters: harborframework.com/docs") {
		t.Fatalf("expected site filter detail, got %q", joined)
	}
	if !containsString(joined, "query: site:harborframework.com/docs BaseInstalledAgent MyInstalledAgent") || !containsString(joined, "job") {
		t.Fatalf("expected query detail, got %q", joined)
	}
}

func TestNarrationLinesWrapInsteadOfTruncating(t *testing.T) {
	renderer := newNarratedTestAgentRenderer()
	renderer.mu.Lock()
	renderer.lastWidth = 80
	renderer.mu.Unlock()
	renderer.Narrate(core.NarrativeEvent{
		Kind:   core.NarrativeIntent,
		Phase:  "Searching the web",
		Text:   "To register IronLark as an installed agent, subclass BaseInstalledAgent and make the class importable, then select it for the benchmark either by passing --agent-import-path module.path:IronLarkInstalledAgent when running Terminal-Bench or by adding an agents entry in a job.yaml that references the import path.",
		Status: core.NarrativeDone,
	})

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	joined := strings.Join(renderer.transcript, "\n")
	if containsString(joined, "benchmark e...") {
		t.Fatalf("expected wrapped narration instead of truncation, got %q", joined)
	}
	if !containsString(joined, "benchmark either by passin") || !containsString(joined, "g --agent-import-path") {
		t.Fatalf("expected wrapped narration content, got %q", joined)
	}
}

func TestNarratedProgressShiftTabTogglesLatestDetails(t *testing.T) {
	renderer := newNarratedTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.Result(core.ActionResult{
		Action:  core.Action{ID: "show-output", Title: "Show output"},
		Summary: "Collected output",
		Stdout:  strings.Repeat("line\n", 8),
	})

	renderer.mu.Lock()
	before := strings.Join(renderer.transcript, "\n")
	renderer.mu.Unlock()
	if !containsString(before, "detail line(s) hidden") {
		t.Fatalf("expected collapsed result details, got %q", before)
	}

	if submitted, done := renderer.handleKey(keyPress{Kind: keyShiftTab}); submitted != "" || done {
		t.Fatalf("expected toggle only, got %q %t", submitted, done)
	}

	renderer.mu.Lock()
	after := strings.Join(renderer.transcript, "\n")
	renderer.mu.Unlock()
	if containsString(after, "detail line(s) hidden") {
		t.Fatalf("expected details to expand after tab, got %q", after)
	}
}

func TestStreamActionOutputMergesIntoActiveActionEntry(t *testing.T) {
	renderer := newNarratedTestAgentRenderer()
	action := core.Action{ID: "inspect-openclaw", Type: core.ActionRunShell, Title: "Inspect OpenClaw", Command: "printf ok"}

	renderer.ActionProgress(action)
	renderer.StreamActionOutput(action, core.ActionOutputChunk{
		ActionID: action.ID,
		Stream:   core.ActionOutputStdout,
		Text:     "openclaw found",
	})
	renderer.Result(core.ActionResult{
		Action:  action,
		Summary: "OpenClaw appears to be installed",
		Stdout:  "openclaw found",
	})

	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	joined := strings.Join(renderer.transcript, "\n")
	if !containsString(joined, "Run(printf ok)") {
		t.Fatalf("expected action entry in transcript, got %q", joined)
	}
	if !containsString(joined, "openclaw found") {
		t.Fatalf("expected streamed output in transcript, got %q", joined)
	}
	if !containsString(joined, "OpenClaw appears to be installed") {
		t.Fatalf("expected merged result summary, got %q", joined)
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
	lines := renderer.promptLinesLocked(80)
	renderer.mu.Unlock()
	line := strings.Join(lines, "\n")
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
	lines := renderer.promptLinesLocked(80)
	renderer.mu.Unlock()
	line := strings.Join(lines, "\n")
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
	promptLines := renderer.promptLinesLocked(80)
	renderer.mu.Unlock()
	promptLine := strings.Join(promptLines, "\n")

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
	promptLines := renderer.promptLinesLocked(80)
	renderer.mu.Unlock()
	promptLine := strings.Join(promptLines, "\n")

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

func TestPromptLinesWrapComposerUpToThreeLines(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.composer = []rune("abcdefghijklmnopqrstuvwxyz1234567890")
	lines := renderer.promptLinesLocked(14)
	renderer.mu.Unlock()

	if len(lines) != 3 {
		t.Fatalf("expected 3 prompt lines, got %d: %#v", len(lines), lines)
	}
	if lines[0] != "> abcdefghijkl" {
		t.Fatalf("unexpected first prompt line %q", lines[0])
	}
	if lines[1] != "  mnopqrstuvwx" {
		t.Fatalf("unexpected second prompt line %q", lines[1])
	}
	if lines[2] != "  yz1234567890" {
		t.Fatalf("unexpected third prompt line %q", lines[2])
	}
}

func TestPromptLinesClipOldestTextAfterThreeLines(t *testing.T) {
	renderer := newTestAgentRenderer()
	renderer.BeginRawTestPrompt()
	renderer.mu.Lock()
	renderer.composer = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	lines := renderer.promptLinesLocked(14)
	renderer.mu.Unlock()

	if len(lines) != 3 {
		t.Fatalf("expected clipped prompt to stay at 3 lines, got %d: %#v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	if containsString(joined, "abc") {
		t.Fatalf("expected oldest text to be clipped from the left, got %q", joined)
	}
	if !strings.HasSuffix(lines[2], "OPQRSTUVWXYZ") {
		t.Fatalf("expected newest text to remain visible, got %#v", lines)
	}
}

func newTestAgentRenderer() *AgentRenderer {
	renderer := NewAgent(nil, discardWriter{}, discardWriter{}, "never", AgentMeta{
		Host:          "srv-1",
		CWD:           "/opt/app",
		Provider:      "openai",
		Model:         "gpt-4.1-mini",
		ModelOptions:  []string{"gpt-4.1-mini", "gpt-5", "gpt-5-mini"},
		ApprovalMode:  "confirm",
		ThreadID:      "thread-1",
		CompactAtRows: 28,
		Interaction:   core.InteractionExecuteFirst,
	})
	renderer.sizeFn = func() (int, int) { return 80, 24 }
	return renderer
}

func newNarratedTestAgentRenderer() *AgentRenderer {
	renderer := NewAgent(nil, discardWriter{}, discardWriter{}, "never", AgentMeta{
		Host:             "srv-1",
		CWD:              "/opt/app",
		Provider:         "openai",
		Model:            "gpt-4.1-mini",
		ModelOptions:     []string{"gpt-4.1-mini", "gpt-5", "gpt-5-mini"},
		ApprovalMode:     "confirm",
		ThreadID:         "thread-1",
		CompactAtRows:    28,
		Interaction:      core.InteractionExecuteFirst,
		NarratedProgress: true,
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
