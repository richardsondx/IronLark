package render

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/richardsondx/IronLark/internal/checkpoints"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/sessions"
	"golang.org/x/term"
)

type AgentMeta struct {
	Host          string
	CWD           string
	Model         string
	ApprovalMode  core.ApprovalMode
	ThreadID      string
	CompactAtRows int
	Interaction   core.InteractionMode
	PolicyStore   policy.Store
}

type overlayKind string

const (
	overlayNone     overlayKind = ""
	overlayMode     overlayKind = "mode"
	overlayApproval overlayKind = "approval"
	overlayPolicy   overlayKind = "policy"
	overlaySlash    overlayKind = "slash"
	overlayBlocker  overlayKind = "blocker"
)

type keyKind int

const (
	keyUnknown keyKind = iota
	keyPrintable
	keyPaste
	keyEnter
	keyBackspace
	keyUp
	keyDown
	keyEscape
	keyTab
	keyShiftTab
)

type keyPress struct {
	Kind keyKind
	Rune rune
	Text string
}

type slashCommand struct {
	Label   string
	Execute string
}

type blockerFocus int

const (
	blockerFocusInput blockerFocus = iota
	blockerFocusSkip
	blockerFocusFollowUp
	blockerFocusFollowUpInput
)

type blockerState struct {
	action        core.Action
	focus         blockerFocus
	answer        []rune
	followUp      []rune
	editingFollow bool
}

type AgentRenderer struct {
	In    *bufio.Reader
	Out   io.Writer
	Err   io.Writer
	Color bool
	JSON  bool

	meta  AgentMeta
	rawIn *os.File

	mu             sync.Mutex
	transcript     []string
	scrollOffset   int
	composer       []rune
	cursor         int
	overlay        overlayKind
	overlayIndex   int
	thinking       bool
	thinkingLabel  string
	thinkingFrame  int
	thinkingStop   chan struct{}
	actionStatus   string
	lastPrompt     string
	slashCommands  []slashCommand
	secretVisible  bool
	readKeyFn      func() (keyPress, error)
	sizeFn         func() (int, int)
	redrawInterval time.Duration
	altScreen      bool
	blocker        *blockerState
}

func NewAgent(in io.Reader, out, err io.Writer, colorMode string, meta AgentMeta) *AgentRenderer {
	renderer := &AgentRenderer{
		In:             bufio.NewReader(in),
		Out:            out,
		Err:            err,
		Color:          shouldUseColor(out, false, colorMode),
		meta:           meta,
		redrawInterval: 100 * time.Millisecond,
		secretVisible:  true,
		slashCommands: []slashCommand{
			{Label: "model", Execute: "/model"},
			{Label: "provider", Execute: "/provider"},
			{Label: "policy", Execute: "/policy"},
			{Label: "clear", Execute: "/clear"},
			{Label: "help", Execute: "/help"},
		},
	}
	if file, ok := in.(*os.File); ok {
		renderer.rawIn = file
	}
	renderer.readKeyFn = renderer.readKey
	renderer.sizeFn = renderer.measure
	return renderer
}

func (r *AgentRenderer) Snapshot(snapshot ctxpkg.Snapshot) error {
	if r.JSON {
		return r.MessageJSON(snapshot)
	}
	r.writeBlock("Inspect", []string{snapshot.JSON()})
	return nil
}

func (r *AgentRenderer) Response(response core.LLMResponse) error {
	lines := []string{r.agentLabel("Summary") + ": " + response.Summary}
	if len(response.Findings) > 0 {
		lines = append(lines, r.agentHeading("Findings")+":")
		for idx, finding := range response.Findings {
			lines = append(lines, fmt.Sprintf("%s %s", r.agentAccent(fmt.Sprintf("%d.", idx+1)), finding))
		}
	}
	r.writeBlock("Lark", lines)
	r.setActionStatus("")
	return nil
}

func (r *AgentRenderer) PlannedActions(actions []core.Action, previews []core.RiskReport) {
	if len(actions) == 0 {
		return
	}
	lines := []string{"Planned actions:"}
	for idx, action := range actions {
		target := action.Command
		if target == "" {
			target = action.Path
		}
		if target == "" {
			target = action.Query
		}
		if target == "" && len(action.Paths) > 0 {
			target = strings.Join(action.Paths, ", ")
		}
		lines = append(lines, fmt.Sprintf("%s %s", r.agentActionTag(string(action.Type)), target))
		if idx < len(previews) {
			lines = append(lines, fmt.Sprintf("%s=%s sudo=%t system=%t rollback=%t",
				r.agentLabel("risk"), r.agentRiskLevel(previews[idx].Level), previews[idx].NeedsSudo, previews[idx].TouchesSystemFiles, previews[idx].RollbackAvailable))
		}
		if action.Reason != "" {
			lines = append(lines, r.agentLabel("why")+": "+action.Reason)
		}
		if action.Type == core.ActionEditFile && action.PatchUnifiedDiff != "" {
			for _, line := range strings.Split(strings.TrimSpace(action.PatchUnifiedDiff), "\n") {
				lines = append(lines, r.formatDiffLine(line))
			}
		}
	}
	r.writeBlock("Plan", lines)
}

func (r *AgentRenderer) ActionProgress(action core.Action) {
	target := action.Title
	if target == "" {
		target = action.Command
	}
	if target == "" {
		target = action.Path
	}
	if target == "" {
		target = string(action.Type)
	}
	r.setActionStatus("Running action...")
	r.writeBlock("Run", []string{fmt.Sprintf("%s %s", r.agentActionTag(string(action.Type)), target)})
}

func (r *AgentRenderer) ApprovalPrompt(action core.Action, report core.RiskReport) {
	lines := []string{
		fmt.Sprintf("%s %s", r.agentActionTag(string(action.Type)), firstNonEmpty(action.Command, action.Path, action.Query, action.Title)),
		fmt.Sprintf("%s=%s sudo=%t system=%t rollback=%t", r.agentLabel("risk"), r.agentRiskLevel(report.Level), report.NeedsSudo, report.TouchesSystemFiles, report.RollbackAvailable),
	}
	if action.Reason != "" {
		lines = append(lines, r.agentLabel("why")+": "+action.Reason)
	}
	if action.Type == core.ActionEditFile && action.PatchUnifiedDiff != "" {
		for _, line := range strings.Split(strings.TrimSpace(action.PatchUnifiedDiff), "\n") {
			lines = append(lines, r.formatDiffLine(line))
		}
	}
	r.setActionStatus("")
	r.writeBlock("Approval needed", lines)
}

func (r *AgentRenderer) Result(result core.ActionResult) {
	status := "ok"
	if result.Error != "" {
		status = "error"
	}
	if result.Skipped {
		status = "skipped"
	}
	lines := []string{fmt.Sprintf("%s %s", r.agentResultTag(status), result.Action.Title)}
	if result.Summary != "" {
		lines = append(lines, r.agentLabel("Summary")+": "+result.Summary)
	}
	if result.Stdout != "" {
		lines = append(lines, r.agentLabel("Output")+":")
		for _, line := range strings.Split(strings.TrimRight(result.Stdout, "\n"), "\n") {
			lines = append(lines, "  "+r.agentOutputPrefix(ansiGray)+" "+r.agentOutputLine(line, ansiGray))
		}
	}
	if result.Stderr != "" {
		lines = append(lines, r.agentLabel("Stderr")+":")
		for _, line := range strings.Split(strings.TrimRight(result.Stderr, "\n"), "\n") {
			lines = append(lines, "  "+r.agentOutputPrefix(ansiRed)+" "+r.agentOutputLine(line, ansiRed))
		}
	}
	if result.Error != "" {
		lines = append(lines, r.agentLabel("Error")+": "+r.agentOutputLine(strings.TrimSpace(result.Error), ansiRed))
	}
	if result.PatchID != "" {
		lines = append(lines, r.agentLabel("Patch ID")+": "+result.PatchID)
	}
	if result.CheckpointID != "" {
		lines = append(lines, r.agentLabel("Checkpoint ID")+": "+result.CheckpointID)
	}
	r.setActionStatus("")
	r.writeBlock("Result", lines)
}

func (r *AgentRenderer) BeginThinking(label string) {
	r.mu.Lock()
	if label == "" {
		label = "Thinking..."
	}
	r.thinking = true
	r.thinkingLabel = label
	r.thinkingFrame = 0
	if r.thinkingStop != nil {
		close(r.thinkingStop)
	}
	stop := make(chan struct{})
	r.thinkingStop = stop
	_ = r.drawLocked()
	r.mu.Unlock()

	go func() {
		ticker := time.NewTicker(r.tickInterval())
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.mu.Lock()
				if !r.thinking || r.thinkingStop != stop {
					r.mu.Unlock()
					return
				}
				r.thinkingFrame++
				_ = r.drawLocked()
				r.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
}

func (r *AgentRenderer) EndThinking() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopThinkingLocked()
	_ = r.drawLocked()
}

func (r *AgentRenderer) SetInteraction(mode core.InteractionMode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta.Interaction = mode
	_ = r.drawLocked()
}

func (r *AgentRenderer) SetApproval(mode core.ApprovalMode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta.ApprovalMode = mode
	_ = r.drawLocked()
}

func (r *AgentRenderer) SetSecretVisibility(visible bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secretVisible = visible
	_ = r.drawLocked()
}

func (r *AgentRenderer) SecretVisibility() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secretVisible {
		return "visible"
	}
	return "hidden"
}

func (r *AgentRenderer) ClearScreen() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transcript = nil
	r.scrollOffset = 0
	r.resetComposerLocked()
	r.overlay = overlayNone
	r.overlayIndex = 0
	r.blocker = nil
	r.actionStatus = ""
	r.stopThinkingLocked()
	_ = r.drawLocked()
}

func (r *AgentRenderer) Sessions(records []sessions.Record) error {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("%s  %s  %s", record.ID, record.StartedAt.Format("2006-01-02 15:04:05"), record.Summary))
	}
	r.writeBlock("Sessions", lines)
	return nil
}

func (r *AgentRenderer) Patches(records []patches.Record) error {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("%s  %s  %s", record.ID, record.CreatedAt.Format("2006-01-02 15:04:05"), record.Path))
	}
	r.writeBlock("Patches", lines)
	return nil
}

func (r *AgentRenderer) Checkpoints(records []checkpoints.Record) error {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("%s  %s  files=%d  %s", record.ID, record.CreatedAt.Format("2006-01-02 15:04:05"), len(record.Files), record.Reason))
	}
	r.writeBlock("Checkpoints", lines)
	return nil
}

func (r *AgentRenderer) PromptChoice() (string, error) {
	r.writeBlock("Choose", []string{"1. Approve all", "2. Approve step by step", "3. Show commands only", "4. Cancel"})
	return r.ReadPrompt("> ")
}

func (r *AgentRenderer) PromptApprovalChoice() (string, error) {
	r.writeBlock("Choose", []string{"1. Allow once", "2. Always allow on this machine", "3. Deny once", "4. Cancel"})
	return r.ReadPrompt("> ")
}

func (r *AgentRenderer) CollectUserInput(action core.Action) (core.ActionResult, error) {
	result := core.ActionResult{
		Action:      action,
		Approved:    true,
		InputKind:   action.InputKind,
		FieldKey:    action.FieldKey,
		IsSensitive: action.InputKind == core.InputSecret,
	}
	if inFile, ok := r.inFile(); ok && term.IsTerminal(int(inFile.Fd())) {
		value, mode, err := r.readBlockerRaw(inFile, action)
		if err != nil {
			return core.ActionResult{}, err
		}
		result.InputValue = value
		result.ResponseMode = mode
		result.Skipped = mode == core.InputResponseSkipped
		result.Summary = blockerSummary(action, mode, value)
		return result, nil
	}
	value, mode, err := r.readBlockerBuffered(action)
	if err != nil {
		return core.ActionResult{}, err
	}
	result.InputValue = value
	result.ResponseMode = mode
	result.Skipped = mode == core.InputResponseSkipped
	result.Summary = blockerSummary(action, mode, value)
	return result, nil
}

func (r *AgentRenderer) Confirm(label string, double bool) (bool, error) {
	prompt := fmt.Sprintf("Run %s? [y/N] ", label)
	if double {
		prompt = fmt.Sprintf("High risk action. Type YES to run %s: ", label)
	}
	answer, err := r.ReadPrompt(prompt)
	if err != nil {
		return false, err
	}
	answer = strings.TrimSpace(answer)
	if double {
		return answer == "YES", nil
	}
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func (r *AgentRenderer) ReadPrompt(prefix string) (string, error) {
	if inFile, ok := r.inFile(); ok && term.IsTerminal(int(inFile.Fd())) {
		return r.readPromptRaw(inFile)
	}
	return r.readPromptBuffered(prefix)
}

func (r *AgentRenderer) Message(text string) {
	r.writeBlock("Info", splitMessageLines(text))
}

func (r *AgentRenderer) MessageJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	r.writeBlock("JSON", []string{string(data)})
	return nil
}

func (r *AgentRenderer) readPromptBuffered(prefix string) (string, error) {
	r.mu.Lock()
	r.lastPrompt = prefix
	_ = r.drawLocked()
	r.mu.Unlock()
	line, err := r.In.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line != "" {
		r.writeUserInput(line)
	}
	return line, nil
}

func (r *AgentRenderer) readSecretBuffered(prefix string) (string, error) {
	fmt.Fprint(r.Out, prefix)
	if inFile, ok := r.inFile(); ok && term.IsTerminal(int(inFile.Fd())) {
		line, err := r.In.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value != "" {
			r.writeUserInput(r.transcriptValue(core.Action{InputKind: core.InputSecret}, value))
		}
		return value, nil
	}
	line, err := r.In.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value != "" {
		r.writeUserInput(r.transcriptValue(core.Action{InputKind: core.InputSecret}, value))
	}
	return value, nil
}

func (r *AgentRenderer) readPromptRaw(inFile *os.File) (string, error) {
	oldState, err := term.MakeRaw(int(inFile.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(inFile.Fd()), oldState)

	r.mu.Lock()
	r.lastPrompt = "> "
	r.overlay = overlayNone
	r.overlayIndex = 0
	r.blocker = nil
	r.ensureAltScreenLocked()
	_ = r.drawLocked()
	r.mu.Unlock()

	for {
		key, err := r.readKeyFn()
		if err != nil {
			return "", err
		}
		submitted, done := r.handleKey(key)
		if done {
			return submitted, nil
		}
	}
}

func (r *AgentRenderer) readBlockerBuffered(action core.Action) (string, core.InputResponseMode, error) {
	r.writeBlock("Input needed", blockerTranscriptLines(action))
	if action.InputKind == core.InputSecret {
		value, err := r.readSecretBuffered("> ")
		if err != nil {
			return "", "", err
		}
		if value == "/" || value == "?" {
			followUp, err := r.readPromptBuffered("clarify> ")
			if err != nil {
				return "", "", err
			}
			return followUp, core.InputResponseFollowUp, nil
		}
		if strings.TrimSpace(value) == "" {
			return "", core.InputResponseSkipped, nil
		}
		return value, core.InputResponseSubmitted, nil
	}
	value, err := r.readPromptBuffered("> ")
	if err != nil {
		return "", "", err
	}
	if value == "/" || value == "?" {
		followUp, err := r.readPromptBuffered("clarify> ")
		if err != nil {
			return "", "", err
		}
		return followUp, core.InputResponseFollowUp, nil
	}
	if strings.TrimSpace(value) == "" {
		return "", core.InputResponseSkipped, nil
	}
	if action.InputKind == core.InputConfirm {
		if strings.EqualFold(value, "y") || strings.EqualFold(value, "yes") {
			return "yes", core.InputResponseSubmitted, nil
		}
		return value, core.InputResponseFollowUp, nil
	}
	return value, core.InputResponseSubmitted, nil
}

func (r *AgentRenderer) readBlockerRaw(inFile *os.File, action core.Action) (string, core.InputResponseMode, error) {
	oldState, err := term.MakeRaw(int(inFile.Fd()))
	if err != nil {
		return "", "", err
	}
	defer term.Restore(int(inFile.Fd()), oldState)

	r.mu.Lock()
	r.lastPrompt = "> "
	r.overlay = overlayBlocker
	r.overlayIndex = 0
	r.blocker = &blockerState{
		action: action,
		focus:  blockerFocusInput,
	}
	r.ensureAltScreenLocked()
	_ = r.drawLocked()
	r.mu.Unlock()

	for {
		key, err := r.readKeyFn()
		if err != nil {
			return "", "", err
		}
		submitted, done := r.handleKey(key)
		if done {
			mode := core.InputResponseSubmitted
			r.mu.Lock()
			if r.blocker != nil && r.blocker.editingFollow {
				mode = core.InputResponseFollowUp
			} else if strings.TrimSpace(submitted) == "" {
				mode = core.InputResponseSkipped
			}
			if r.blocker != nil && r.blocker.focus == blockerFocusFollowUpInput {
				mode = core.InputResponseFollowUp
			}
			r.blocker = nil
			r.overlay = overlayNone
			_ = r.drawLocked()
			r.mu.Unlock()
			if action.InputKind == core.InputConfirm && mode == core.InputResponseSubmitted {
				if strings.EqualFold(submitted, "y") || strings.EqualFold(submitted, "yes") {
					return "yes", mode, nil
				}
				return submitted, core.InputResponseFollowUp, nil
			}
			return submitted, mode, nil
		}
	}
}

func (r *AgentRenderer) handleKey(key keyPress) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.overlay == overlayBlocker && r.blocker != nil {
		return r.handleBlockerKeyLocked(key)
	}

	switch key.Kind {
	case keyPrintable:
		r.insertRuneLocked(key.Rune)
	case keyPaste:
		r.insertTextLocked(key.Text)
	case keyBackspace:
		r.backspaceLocked()
	case keyEscape:
		r.overlay = overlayNone
	case keyUp:
		switch r.overlay {
		case overlayMode:
			r.moveOverlayLocked(-1)
		case overlayApproval:
			r.moveOverlayLocked(-1)
		case overlayPolicy:
			r.moveOverlayLocked(-1)
		case overlaySlash:
			r.moveOverlayLocked(-1)
		default:
			r.scrollLocked(1)
		}
	case keyDown:
		switch r.overlay {
		case overlayMode:
			r.moveOverlayLocked(1)
		case overlayApproval:
			r.moveOverlayLocked(1)
		case overlayPolicy:
			r.moveOverlayLocked(1)
		case overlaySlash:
			r.moveOverlayLocked(1)
		default:
			r.scrollLocked(-1)
		}
	case keyTab, keyShiftTab:
		// Ignore in the main composer; blocker mode handles keyboard focus.
	case keyEnter:
		switch r.overlay {
		case overlayMode:
			command := r.selectedModeCommandLocked()
			r.overlay = overlayNone
			r.writeUserInputLocked(command)
			_ = r.drawLocked()
			return command, true
		case overlayApproval:
			command := r.selectedApprovalCommandLocked()
			r.overlay = overlayNone
			r.writeUserInputLocked(command)
			_ = r.drawLocked()
			return command, true
		case overlayPolicy:
			r.toggleSelectedPolicyRuleLocked()
			_ = r.drawLocked()
			return "", false
		case overlaySlash:
			command := r.selectedSlashCommandLocked()
			if command == "/approval" {
				r.openApprovalOverlayLocked()
				_ = r.drawLocked()
				return "", false
			}
			if command == "/policy" {
				r.openPolicyOverlayLocked()
				_ = r.drawLocked()
				return "", false
			}
			if command != "" {
				r.resetComposerLocked()
				r.overlay = overlayNone
				r.writeUserInputLocked(command)
				_ = r.drawLocked()
				return command, true
			}
		default:
			prompt := strings.TrimSpace(string(r.composer))
			r.resetComposerLocked()
			r.overlay = overlayNone
			if prompt != "" {
				r.writeUserInputLocked(prompt)
			}
			_ = r.drawLocked()
			return prompt, true
		}
	}

	r.updateOverlayLocked()
	_ = r.drawLocked()
	return "", false
}

func (r *AgentRenderer) handleBlockerKeyLocked(key keyPress) (string, bool) {
	if r.blocker == nil {
		return "", false
	}
	switch key.Kind {
	case keyPrintable:
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			r.blocker.followUp = append(r.blocker.followUp, key.Rune)
			r.blocker.focus = blockerFocusFollowUpInput
		} else {
			r.blocker.answer = append(r.blocker.answer, key.Rune)
			r.blocker.focus = blockerFocusInput
		}
	case keyPaste:
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			r.blocker.followUp = append(r.blocker.followUp, []rune(key.Text)...)
			r.blocker.focus = blockerFocusFollowUpInput
		} else {
			r.blocker.answer = append(r.blocker.answer, []rune(key.Text)...)
			r.blocker.focus = blockerFocusInput
		}
	case keyBackspace:
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			if len(r.blocker.followUp) > 0 {
				r.blocker.followUp = r.blocker.followUp[:len(r.blocker.followUp)-1]
			}
		} else if len(r.blocker.answer) > 0 {
			r.blocker.answer = r.blocker.answer[:len(r.blocker.answer)-1]
		}
	case keyTab:
		r.advanceBlockerFocusLocked(1)
	case keyShiftTab:
		r.advanceBlockerFocusLocked(-1)
	case keyEscape:
		if r.blocker.editingFollow {
			r.blocker.editingFollow = false
			r.blocker.focus = blockerFocusFollowUp
		}
	case keyEnter:
		switch r.blocker.focus {
		case blockerFocusSkip:
			_ = r.drawLocked()
			return "", true
		case blockerFocusFollowUp:
			r.blocker.editingFollow = true
			r.blocker.focus = blockerFocusFollowUpInput
		case blockerFocusFollowUpInput:
			value := strings.TrimSpace(string(r.blocker.followUp))
			if value == "" {
				r.blocker.editingFollow = false
				r.blocker.focus = blockerFocusFollowUp
				_ = r.drawLocked()
				return "", false
			}
			r.writeUserInputLocked(value)
			_ = r.drawLocked()
			return value, true
		default:
			value := strings.TrimSpace(string(r.blocker.answer))
			if value == "" {
				_ = r.drawLocked()
				return "", true
			}
			r.writeUserInputLocked(r.transcriptValueLocked(r.blocker.action, value))
			_ = r.drawLocked()
			return value, true
		}
	}
	_ = r.drawLocked()
	return "", false
}

func (r *AgentRenderer) BeginRawTestPrompt() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastPrompt = "> "
	r.overlay = overlayNone
	r.overlayIndex = 0
	r.scrollOffset = 0
}

func (r *AgentRenderer) currentModeLabel() string {
	switch r.meta.Interaction {
	case core.InteractionPlanFirst:
		return "plan-first"
	default:
		return "execute-first"
	}
}

func (r *AgentRenderer) insertRuneLocked(ch rune) {
	r.composer = append(r.composer, ch)
	r.cursor = len(r.composer)
}

func (r *AgentRenderer) insertTextLocked(text string) {
	r.composer = append(r.composer, []rune(text)...)
	r.cursor = len(r.composer)
}

func (r *AgentRenderer) backspaceLocked() {
	if len(r.composer) == 0 {
		return
	}
	r.composer = r.composer[:len(r.composer)-1]
	r.cursor = len(r.composer)
}

func (r *AgentRenderer) openModeOverlayLocked() {
	r.overlay = overlayMode
	if r.meta.Interaction == core.InteractionPlanFirst {
		r.overlayIndex = 1
	} else {
		r.overlayIndex = 0
	}
}

func (r *AgentRenderer) openApprovalOverlayLocked() {
	r.overlay = overlayApproval
	r.overlayIndex = r.approvalIndexLocked()
}

func (r *AgentRenderer) openPolicyOverlayLocked() {
	r.overlay = overlayPolicy
	r.overlayIndex = 0
	r.clampPolicyIndexLocked()
}

func (r *AgentRenderer) updateOverlayLocked() {
	composer := string(r.composer)
	if r.overlay == overlayMode || r.overlay == overlayApproval || r.overlay == overlayPolicy || r.overlay == overlayBlocker {
		return
	}
	if strings.HasPrefix(composer, "/") {
		r.overlay = overlaySlash
		commands := r.filteredSlashCommandsLocked()
		if len(commands) == 0 {
			r.overlayIndex = 0
			return
		}
		if r.overlayIndex >= len(commands) {
			r.overlayIndex = len(commands) - 1
		}
		if r.overlayIndex < 0 {
			r.overlayIndex = 0
		}
		return
	}
	if r.overlay == overlaySlash {
		r.overlay = overlayNone
		r.overlayIndex = 0
	}
}

func (r *AgentRenderer) moveOverlayLocked(delta int) {
	var size int
	switch r.overlay {
	case overlayMode:
		size = 2
	case overlayApproval:
		size = len(approvalModes)
	case overlayPolicy:
		size = len(r.policyRulesLocked())
	case overlaySlash:
		size = len(r.filteredSlashCommandsLocked())
	default:
		return
	}
	if size == 0 {
		r.overlayIndex = 0
		return
	}
	r.overlayIndex = (r.overlayIndex + delta + size) % size
}

func (r *AgentRenderer) selectedModeCommandLocked() string {
	if r.overlayIndex == 1 {
		return "/mode plan-first"
	}
	return "/mode execute-first"
}

func (r *AgentRenderer) secretToggleCommandLocked() slashCommand {
	if r.secretVisible {
		return slashCommand{
			Label:   "secret: visible",
			Execute: "/secret hidden",
		}
	}
	return slashCommand{
		Label:   "secret: hidden",
		Execute: "/secret visible",
	}
}

func (r *AgentRenderer) selectedApprovalCommandLocked() string {
	if r.overlayIndex < 0 || r.overlayIndex >= len(approvalModes) {
		r.overlayIndex = 0
	}
	return "/approval " + string(approvalModes[r.overlayIndex])
}

func (r *AgentRenderer) selectedSlashCommandLocked() string {
	commands := r.filteredSlashCommandsLocked()
	if len(commands) == 0 {
		return strings.TrimSpace(string(r.composer))
	}
	if r.overlayIndex < 0 || r.overlayIndex >= len(commands) {
		r.overlayIndex = 0
	}
	return commands[r.overlayIndex].Execute
}

func (r *AgentRenderer) filteredSlashCommandsLocked() []slashCommand {
	if !strings.HasPrefix(string(r.composer), "/") {
		return nil
	}
	filter := strings.ToLower(strings.TrimPrefix(string(r.composer), "/"))
	commands := make([]slashCommand, 0, len(r.slashCommands)+1)
	modeCommand := r.modeToggleCommandLocked()
	if filter == "" || strings.Contains(strings.ToLower(modeCommand.Label), filter) || strings.Contains("mode", filter) {
		commands = append(commands, modeCommand)
	}
	approvalCommand := r.approvalToggleCommandLocked()
	if filter == "" || strings.Contains(strings.ToLower(approvalCommand.Label), filter) || strings.Contains("approval", filter) {
		commands = append(commands, approvalCommand)
	}
	secretCommand := r.secretToggleCommandLocked()
	if filter == "" || strings.Contains(strings.ToLower(secretCommand.Label), filter) || strings.Contains("secret", filter) {
		commands = append(commands, secretCommand)
	}
	for _, command := range r.slashCommands {
		if filter == "" || strings.Contains(strings.ToLower(command.Label), filter) {
			commands = append(commands, command)
		}
	}
	sort.SliceStable(commands, func(i, j int) bool {
		left := strings.ToLower(commands[i].Label)
		right := strings.ToLower(commands[j].Label)
		if strings.HasPrefix(left, "mode:") != strings.HasPrefix(right, "mode:") {
			return strings.HasPrefix(left, "mode:")
		}
		if strings.HasPrefix(left, "approval:") != strings.HasPrefix(right, "approval:") {
			return strings.HasPrefix(left, "approval:")
		}
		leftStarts := strings.HasPrefix(left, filter)
		rightStarts := strings.HasPrefix(right, filter)
		if leftStarts != rightStarts {
			return leftStarts
		}
		return left < right
	})
	return commands
}

func (r *AgentRenderer) writeUserInput(prompt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeUserInputLocked(prompt)
	_ = r.drawLocked()
}

func (r *AgentRenderer) writeUserInputLocked(prompt string) {
	lines := splitMessageLines(prompt)
	for idx, line := range lines {
		prefix := "  "
		if idx == 0 {
			prefix = "> "
		}
		r.transcript = append(r.transcript, prefix+line)
	}
}

func (r *AgentRenderer) resetComposerLocked() {
	r.composer = nil
	r.cursor = 0
}

func (r *AgentRenderer) advanceBlockerFocusLocked(delta int) {
	if r.blocker == nil {
		return
	}
	order := []blockerFocus{blockerFocusInput, blockerFocusSkip, blockerFocusFollowUp}
	current := 0
	for idx, focus := range order {
		if focus == r.blocker.focus || (r.blocker.focus == blockerFocusFollowUpInput && focus == blockerFocusFollowUp) {
			current = idx
			break
		}
	}
	current = (current + delta + len(order)) % len(order)
	r.blocker.focus = order[current]
	r.blocker.editingFollow = false
}

func (r *AgentRenderer) setActionStatus(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actionStatus = status
	_ = r.drawLocked()
}

func (r *AgentRenderer) stopThinkingLocked() {
	r.thinking = false
	r.thinkingLabel = ""
	r.thinkingFrame = 0
	if r.thinkingStop != nil {
		close(r.thinkingStop)
		r.thinkingStop = nil
	}
}

func (r *AgentRenderer) writeBlock(title string, lines []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(lines) == 0 {
		lines = []string{""}
	}
	r.transcript = append(r.transcript, "")
	r.transcript = append(r.transcript, "## "+title)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			r.transcript = append(r.transcript, "")
			continue
		}
		for _, wrapped := range r.wrapWithWidth(line, r.bodyWidth()) {
			r.transcript = append(r.transcript, wrapped)
		}
	}
	_ = r.drawLocked()
}

func (r *AgentRenderer) drawLocked() error {
	width, height := r.sizeFn()
	header := r.headerLines(width, height)
	bodyHeight := height - len(header) - 2
	if bodyHeight < 4 {
		bodyHeight = 4
	}

	overlayLines := r.overlayLinesLocked(width)
	bodyHeight -= len(overlayLines)
	if bodyHeight < 2 {
		bodyHeight = 2
	}

	start := 0
	maxStart := len(r.transcript) - bodyHeight
	if maxStart < 0 {
		maxStart = 0
	}
	if r.scrollOffset > maxStart {
		r.scrollOffset = maxStart
	}
	if len(r.transcript) > bodyHeight {
		start = maxStart - r.scrollOffset
		if start < 0 {
			start = 0
		}
	}
	end := len(r.transcript)
	if bodyHeight > 0 && start+bodyHeight < end {
		end = start + bodyHeight
	}
	body := r.transcript[start:end]

	r.ensureAltScreenLocked()
	if _, err := fmt.Fprint(r.Out, "\033[H\033[2J"); err != nil {
		return err
	}
	for _, line := range header {
		if err := r.writeScreenLine(truncateDisplay(line, width)); err != nil {
			return err
		}
	}
	for _, line := range body {
		if err := r.writeScreenLine(truncateDisplay(line, width)); err != nil {
			return err
		}
	}
	for i := len(body); i < bodyHeight; i++ {
		if err := r.writeScreenLine(""); err != nil {
			return err
		}
	}
	for _, line := range overlayLines {
		if err := r.writeScreenLine(truncateDisplay(line, width)); err != nil {
			return err
		}
	}
	if err := r.writeScreenLine(truncateDisplay(r.statusLineLocked(width), width)); err != nil {
		return err
	}
	return r.writeFinalScreenLine(truncateDisplay(r.promptLineLocked(width), width))
}

func (r *AgentRenderer) headerLines(width, height int) []string {
	hostLine := fmt.Sprintf(" IronLark Agent  host=%s  cwd=%s ", r.meta.Host, r.meta.CWD)
	statusLine := fmt.Sprintf(" model=%s  approval=%s  mode=%s  thread=%s ", r.meta.Model, r.meta.ApprovalMode, r.currentModeLabel(), r.meta.ThreadID)
	if r.meta.CompactAtRows > 0 && height <= r.meta.CompactAtRows {
		return []string{truncateDisplay(hostLine, width), truncateDisplay(statusLine, width)}
	}
	return []string{
		truncateDisplay(hostLine, width),
		truncateDisplay(statusLine, width),
		truncateDisplay(" SSH-first operator workspace. Up/down scrolls output. Type / for command menu. ", width),
	}
}

func (r *AgentRenderer) statusLineLocked(width int) string {
	base := strings.Repeat("-", clamp(width, 12, width))
	switch {
	case r.thinking:
		label := thinkingFrames[r.thinkingFrame%len(thinkingFrames)] + " " + r.thinkingLabel
		return truncate(statusWithLabel(width, label), width)
	case r.actionStatus != "":
		return truncate(statusWithLabel(width, r.actionStatus), width)
	default:
		return base
	}
}

func (r *AgentRenderer) promptLineLocked(width int) string {
	if r.overlay == overlayBlocker && r.blocker != nil {
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			return "clarify> " + renderPromptValue(string(r.blocker.followUp))
		}
		value := string(r.blocker.answer)
		if r.blocker.action.InputKind == core.InputSecret && !r.secretVisible {
			value = strings.Repeat("*", len(r.blocker.answer))
		}
		return "> " + renderPromptValue(value)
	}
	return "> " + renderPromptValue(string(r.composer))
}

func (r *AgentRenderer) overlayLinesLocked(width int) []string {
	switch r.overlay {
	case overlayMode:
		return r.wrapOverlayLines(width, []string{
			"Mode",
			r.overlayOptionLocked(0, "mode: execute-first", ""),
			r.overlayOptionLocked(1, "mode: plan-first", ""),
		})
	case overlayApproval:
		lines := []string{"Approval"}
		for idx, mode := range approvalModes {
			lines = append(lines, r.overlayOptionLocked(idx, "approval: "+string(mode), ""))
		}
		return r.wrapOverlayLines(width, lines)
	case overlayPolicy:
		rules := r.policyRulesLocked()
		if len(rules) == 0 {
			return r.wrapOverlayLines(width, []string{"Policy", "  no machine rules yet"})
		}
		lines := []string{"Policy"}
		for idx, rule := range rules {
			lines = append(lines, r.overlayOptionLocked(idx, formatPolicyRule(rule), ""))
		}
		lines = append(lines, "  enter toggles allow/deny")
		return r.wrapOverlayLines(width, lines)
	case overlaySlash:
		commands := r.filteredSlashCommandsLocked()
		if len(commands) == 0 {
			return r.wrapOverlayLines(width, []string{"Commands", "  no matching commands"})
		}
		lines := []string{"Commands"}
		for idx, command := range commands {
			lines = append(lines, r.overlayOptionLocked(idx, command.Label, ""))
		}
		return r.wrapOverlayLines(width, lines)
	case overlayBlocker:
		return r.wrapOverlayLines(width, r.blockerLinesLocked())
	default:
		return nil
	}
}

func (r *AgentRenderer) blockerLinesLocked() []string {
	if r.blocker == nil {
		return nil
	}
	action := r.blocker.action
	lines := blockerTranscriptLines(action)
	lines = append(lines,
		r.blockerOptionLocked(blockerFocusInput, "Input"),
		r.blockerOptionLocked(blockerFocusSkip, "Skip"),
		r.blockerOptionLocked(blockerFocusFollowUp, "Follow-up"),
		"  tab cycles options, enter activates the focused control",
	)
	return lines
}

func (r *AgentRenderer) blockerOptionLocked(focus blockerFocus, label string) string {
	prefix := "  "
	if r.blocker != nil {
		active := r.blocker.focus
		if active == blockerFocusFollowUpInput {
			active = blockerFocusFollowUp
		}
		if active == focus {
			prefix = "> "
		}
	}
	return prefix + label
}

func (r *AgentRenderer) overlayOptionLocked(index int, label, detail string) string {
	prefix := "  "
	if r.overlayIndex == index {
		prefix = "> "
	}
	if detail == "" {
		return prefix + label
	}
	return prefix + label + "  " + detail
}

func (r *AgentRenderer) wrapOverlayLines(width int, lines []string) []string {
	bodyWidth := clamp(width-6, 20, width)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, truncateDisplay(line, bodyWidth))
	}
	return out
}

func (r *AgentRenderer) modeToggleCommandLocked() slashCommand {
	if r.meta.Interaction == core.InteractionPlanFirst {
		return slashCommand{
			Label:   "mode: plan-first",
			Execute: "/mode execute-first",
		}
	}
	return slashCommand{
		Label:   "mode: execute-first",
		Execute: "/mode plan-first",
	}
}

func (r *AgentRenderer) approvalToggleCommandLocked() slashCommand {
	return slashCommand{
		Label:   "approval: " + string(r.meta.ApprovalMode),
		Execute: "/approval",
	}
}

func (r *AgentRenderer) approvalIndexLocked() int {
	for idx, mode := range approvalModes {
		if mode == r.meta.ApprovalMode {
			return idx
		}
	}
	return 0
}

func (r *AgentRenderer) policyRulesLocked() []policy.Rule {
	rules, err := r.meta.PolicyStore.List()
	if err != nil {
		return nil
	}
	return rules
}

func (r *AgentRenderer) clampPolicyIndexLocked() {
	rules := r.policyRulesLocked()
	if len(rules) == 0 {
		r.overlayIndex = 0
		return
	}
	if r.overlayIndex < 0 {
		r.overlayIndex = 0
	}
	if r.overlayIndex >= len(rules) {
		r.overlayIndex = len(rules) - 1
	}
}

func (r *AgentRenderer) toggleSelectedPolicyRuleLocked() {
	rules := r.policyRulesLocked()
	if len(rules) == 0 {
		return
	}
	r.clampPolicyIndexLocked()
	selected := rules[r.overlayIndex]
	_ = r.meta.PolicyStore.Remove(selected.ID)
	if selected.Decision == policy.DecisionAllow {
		selected.Decision = policy.DecisionDeny
	} else {
		selected.Decision = policy.DecisionAllow
	}
	selected.ID = ""
	selected.CreatedAt = time.Time{}
	_, _ = r.meta.PolicyStore.Add(selected)
}

func (r *AgentRenderer) wrapWithWidth(line string, width int) []string {
	if strings.Contains(line, "\033[") {
		return []string{line}
	}
	if width <= 0 || visibleWidth(line) <= width {
		return []string{line}
	}
	out := []string{}
	for visibleWidth(line) > width {
		out = append(out, line[:width])
		line = line[width:]
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func splitMessageLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(text, "\n")
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func (r *AgentRenderer) writeScreenLine(line string) error {
	_, err := fmt.Fprintf(r.Out, "\r%s\r\n", line)
	return err
}

func (r *AgentRenderer) writeFinalScreenLine(line string) error {
	_, err := fmt.Fprintf(r.Out, "\r%s", line)
	return err
}

func (r *AgentRenderer) ensureAltScreenLocked() {
	if r.altScreen {
		return
	}
	_, _ = fmt.Fprint(r.Out, "\033[?1049h\033[?2004h")
	r.altScreen = true
}

func (r *AgentRenderer) scrollLocked(delta int) {
	next := r.scrollOffset + delta
	if next < 0 {
		next = 0
	}
	r.scrollOffset = next
}

func (r *AgentRenderer) bodyWidth() int {
	width, _ := r.sizeFn()
	return clamp(width-2, 20, width)
}

func (r *AgentRenderer) measure() (int, int) {
	width, height := 80, 24
	if file, ok := r.Out.(*os.File); ok {
		if w, h, err := term.GetSize(int(file.Fd())); err == nil {
			width, height = w, h
		}
	}
	return width, height
}

func (r *AgentRenderer) inFile() (*os.File, bool) {
	return r.rawIn, r.rawIn != nil
}

func (r *AgentRenderer) readKey() (keyPress, error) {
	b, err := r.In.ReadByte()
	if err != nil {
		return keyPress{}, err
	}
	switch b {
	case '\r', '\n':
		return keyPress{Kind: keyEnter}, nil
	case '\t':
		return keyPress{Kind: keyTab}, nil
	case 127, 8:
		return keyPress{Kind: keyBackspace}, nil
	case 27:
		next, ok := r.readEscByte()
		if !ok {
			return keyPress{Kind: keyEscape}, nil
		}
		if next == '[' {
			code, ok := r.readEscByte()
			if !ok {
				return keyPress{Kind: keyEscape}, nil
			}
			switch code {
			case 'A':
				return keyPress{Kind: keyUp}, nil
			case 'B':
				return keyPress{Kind: keyDown}, nil
			case 'Z':
				return keyPress{Kind: keyShiftTab}, nil
			case '2':
				if r.readEscapeSequence("00~") {
					text, err := r.readBracketedPaste()
					if err != nil {
						return keyPress{}, err
					}
					return keyPress{Kind: keyPaste, Text: text}, nil
				}
			}
		}
		if next == 'O' {
			code, ok := r.readEscByte()
			if !ok {
				return keyPress{Kind: keyEscape}, nil
			}
			switch code {
			case 'A':
				return keyPress{Kind: keyUp}, nil
			case 'B':
				return keyPress{Kind: keyDown}, nil
			}
		}
		return keyPress{Kind: keyEscape}, nil
	default:
		if b >= 32 && b <= 126 {
			return keyPress{Kind: keyPrintable, Rune: rune(b)}, nil
		}
	}
	return keyPress{Kind: keyUnknown}, nil
}

func (r *AgentRenderer) readEscapeSequence(want string) bool {
	for i := 0; i < len(want); i++ {
		b, ok := r.readEscByte()
		if !ok || b != want[i] {
			return false
		}
	}
	return true
}

func (r *AgentRenderer) readBracketedPaste() (string, error) {
	const endMarker = "\x1b[201~"
	data := make([]byte, 0, 64)
	for {
		b, err := r.In.ReadByte()
		if err != nil {
			return "", err
		}
		data = append(data, b)
		if len(data) >= len(endMarker) && string(data[len(data)-len(endMarker):]) == endMarker {
			return string(data[:len(data)-len(endMarker)]), nil
		}
	}
}

func (r *AgentRenderer) readEscByte() (byte, bool) {
	for i := 0; i < 25; i++ {
		if r.In.Buffered() > 0 {
			b, err := r.In.ReadByte()
			if err == nil {
				return b, true
			}
			return 0, false
		}
		time.Sleep(time.Millisecond)
	}
	return 0, false
}

func renderPromptValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\\n")
}

func (r *AgentRenderer) tickInterval() time.Duration {
	if r.redrawInterval <= 0 {
		return 100 * time.Millisecond
	}
	return r.redrawInterval
}

var thinkingFrames = []string{"_*_", "__*", "_*_", "*__"}

var approvalModes = []core.ApprovalMode{
	core.ApprovalConfirm,
	core.ApprovalAutoSafe,
	core.ApprovalAgent,
	core.ApprovalSuggest,
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncate(value string, width int) string {
	return truncateDisplay(value, width)
}

func truncateDisplay(value string, width int) string {
	if width <= 0 || visibleWidth(value) <= width {
		return value
	}
	if width <= 1 {
		return truncateVisible(value, width)
	}
	if width <= 3 {
		return truncateVisible(value, width)
	}
	return truncateVisible(value, width-3) + "..."
}

func truncateVisible(value string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	visible := 0
	for i := 0; i < len(value); {
		if value[i] == 27 {
			end := ansiSequenceEnd(value, i)
			if end <= i {
				break
			}
			b.WriteString(value[i:end])
			i = end
			continue
		}
		if visible >= width {
			break
		}
		b.WriteByte(value[i])
		i++
		visible++
	}
	if strings.Contains(b.String(), "\033[") && !strings.HasSuffix(b.String(), ansiReset) {
		b.WriteString(ansiReset)
	}
	return b.String()
}

func visibleWidth(value string) int {
	width := 0
	for i := 0; i < len(value); {
		if value[i] == 27 {
			end := ansiSequenceEnd(value, i)
			if end <= i {
				i++
				continue
			}
			i = end
			continue
		}
		i++
		width++
	}
	return width
}

func ansiSequenceEnd(value string, start int) int {
	if start+1 >= len(value) || value[start] != 27 || value[start+1] != '[' {
		return start + 1
	}
	i := start + 2
	for i < len(value) {
		ch := value[i]
		if ch >= '@' && ch <= '~' {
			return i + 1
		}
		i++
	}
	return len(value)
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if maximum > 0 && value > maximum {
		return maximum
	}
	return value
}

func formatPolicyRule(rule policy.Rule) string {
	return fmt.Sprintf("%s  %s  %s", rule.Decision, rule.Kind, rule.Value)
}

func statusWithLabel(width int, label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return strings.Repeat("-", clamp(width, 12, width))
	}
	pad := len(label) + 3
	baseWidth := clamp(width-pad, 12, width)
	return strings.Repeat("-", baseWidth) + " " + label
}

func (r *AgentRenderer) formatDiffLine(line string) string {
	if !r.Color {
		return line
	}
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		return colorize(line, ansiBold)
	case strings.HasPrefix(line, "@@"):
		return colorize(line, ansiCyan)
	case strings.HasPrefix(line, "+"):
		return colorize(line, ansiGreen)
	case strings.HasPrefix(line, "-"):
		return colorize(line, ansiRed)
	default:
		return line
	}
}

func (r *AgentRenderer) agentHeading(text string) string {
	return r.agentStyle(text, ansiBold, ansiCyan)
}

func (r *AgentRenderer) agentLabel(text string) string {
	return r.agentStyle(text, ansiBold)
}

func (r *AgentRenderer) agentAccent(text string) string {
	return r.agentStyle(text, ansiCyan)
}

func (r *AgentRenderer) agentActionTag(text string) string {
	return r.agentStyle("["+text+"]", ansiBold, ansiBlue)
}

func (r *AgentRenderer) agentResultTag(status string) string {
	code := ansiBlue
	switch status {
	case "ok":
		code = ansiGreen
	case "error":
		code = ansiRed
	case "skipped":
		code = ansiYellow
	}
	return r.agentStyle("["+status+"]", ansiBold, code)
}

func (r *AgentRenderer) agentRiskLevel(level core.RiskLevel) string {
	code := ansiBlue
	switch level {
	case core.RiskLow:
		code = ansiGreen
	case core.RiskMedium:
		code = ansiYellow
	case core.RiskHigh:
		code = ansiRed
	}
	return r.agentStyle(string(level), ansiBold, code)
}

func (r *AgentRenderer) agentOutputPrefix(lineColor string) string {
	return r.agentStyle("│", ansiBold, lineColor)
}

func (r *AgentRenderer) agentOutputLine(text, lineColor string) string {
	return r.agentStyle(text, lineColor)
}

func (r *AgentRenderer) agentStyle(text string, codes ...string) string {
	if !r.Color {
		return text
	}
	return strings.Join(codes, "") + text + ansiReset
}

func blockerTranscriptLines(action core.Action) []string {
	lines := []string{}
	if action.Prompt != "" {
		lines = append(lines, action.Prompt)
	}
	if action.Reason != "" && action.Reason != action.Prompt {
		lines = append(lines, "why: "+action.Reason)
	}
	if action.Clarification != "" {
		lines = append(lines, "note: "+action.Clarification)
	}
	if action.DestinationHint != "" {
		lines = append(lines, "next: "+action.DestinationHint)
	}
	if action.Placeholder != "" && action.InputKind != core.InputSecret {
		lines = append(lines, "example: "+action.Placeholder)
	}
	if action.InputKind == core.InputManualWait {
		if action.ExpectsValue {
			lines = append(lines, "return with the requested value when ready")
		} else {
			lines = append(lines, "type yes when done, enter on empty input to skip")
		}
	}
	return lines
}

func blockerSummary(action core.Action, mode core.InputResponseMode, value string) string {
	switch mode {
	case core.InputResponseSkipped:
		return "user skipped input"
	case core.InputResponseFollowUp:
		return "user added follow-up clarification"
	default:
		if action.InputKind == core.InputSecret {
			return "user provided secret input"
		}
		if action.InputKind == core.InputConfirm {
			return "user confirmed"
		}
		return "user provided input"
	}
}

func (r *AgentRenderer) transcriptValue(action core.Action, value string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.transcriptValueLocked(action, value)
}

func (r *AgentRenderer) transcriptValueLocked(action core.Action, value string) string {
	if action.InputKind != core.InputSecret || r.secretVisible {
		return value
	}
	if value == "" {
		return ""
	}
	return "[secret]"
}
