package render

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/richardsondx/IronLark/internal/buildinfo"
	"github.com/richardsondx/IronLark/internal/checkpoints"
	ctxpkg "github.com/richardsondx/IronLark/internal/context"
	"github.com/richardsondx/IronLark/internal/core"
	"github.com/richardsondx/IronLark/internal/patches"
	"github.com/richardsondx/IronLark/internal/policy"
	"github.com/richardsondx/IronLark/internal/sessions"
	"golang.org/x/term"
)

type AgentMeta struct {
	Host             string
	CWD              string
	Provider         string
	Model            string
	ModelOptions     []string
	RecentPrompts    []string
	WelcomeBack      bool
	ApprovalMode     core.ApprovalMode
	ThreadID         string
	CompactAtRows    int
	Interaction      core.InteractionMode
	NarratedProgress bool
	PolicyStore      policy.Store
	OpsSummary       string
}

type overlayKind string

const (
	overlayNone             overlayKind = ""
	overlayHistory          overlayKind = "history"
	overlayMode             overlayKind = "mode"
	overlayApproval         overlayKind = "approval"
	overlayApprovalDecision overlayKind = "approval_decision"
	overlayModel            overlayKind = "model"
	overlayPolicy           overlayKind = "policy"
	overlaySlash            overlayKind = "slash"
	overlayBlocker          overlayKind = "blocker"
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
	keyPageUp
	keyPageDown
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

type approvalDecisionState struct {
	selected int
	level    core.RiskLevel
	result   core.ApprovalDecision
	done     bool
}

type AgentRenderer struct {
	In    *bufio.Reader
	Out   io.Writer
	Err   io.Writer
	Color bool
	JSON  bool

	meta  AgentMeta
	rawIn *os.File

	mu               sync.Mutex
	transcript       []string
	entries          []TranscriptEntry
	scrollOffset     int
	composer         []rune
	cursor           int
	promptHistory    []string
	historyIndex     int
	historyDraft     []rune
	overlay          overlayKind
	overlayIndex     int
	thinking         bool
	thinkingLabel    string
	thinkingFrame    int
	thinkingStop     chan struct{}
	actionStatus     string
	actionFrame      int
	actionStop       chan struct{}
	activePhase      string
	lastPrompt       string
	slashCommands    []slashCommand
	secretVisible    bool
	readKeyFn        func() (keyPress, error)
	sizeFn           func() (int, int)
	redrawInterval   time.Duration
	altScreen        bool
	blocker          *blockerState
	nowFn            func() time.Time
	lastFrame        []string
	lastWidth        int
	cursorHidden     bool
	approvalDecision *approvalDecisionState
}

func NewAgent(in io.Reader, out, err io.Writer, colorMode string, meta AgentMeta) *AgentRenderer {
	renderer := &AgentRenderer{
		In:             bufio.NewReader(in),
		Out:            out,
		Err:            err,
		Color:          shouldUseAgentColor(out, colorMode),
		meta:           meta,
		redrawInterval: 100 * time.Millisecond,
		secretVisible:  true,
		slashCommands: []slashCommand{
			{Label: "model", Execute: "/model"},
			{Label: "provider", Execute: "/provider"},
			{Label: "policy", Execute: "/policy"},
			{Label: "ops", Execute: "/ops"},
			{Label: "watch", Execute: "/watch "},
			{Label: "recover", Execute: "/recover "},
			{Label: "clear", Execute: "/clear"},
			{Label: "help", Execute: "/help"},
			{Label: "exit", Execute: "/exit"},
		},
		nowFn: time.Now,
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
	details := make([]string, 0, len(response.Findings))
	for _, finding := range distinctFindings(response.Summary, response.Findings) {
		if strings.TrimSpace(finding) == "" {
			continue
		}
		details = append(details, "- "+finding)
	}
	r.mu.Lock()
	r.appendEntryLocked(TranscriptEntry{
		Kind:    transcriptEntryAssistant,
		Summary: response.Summary,
		Details: details,
	})
	_ = r.drawLocked()
	r.mu.Unlock()
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
	label := firstNonEmpty(action.Title, action.Command, action.Path, action.Query, string(action.Type))
	if !r.meta.NarratedProgress {
		r.setActionStatus("Running " + label + "...")
		r.mu.Lock()
		r.appendEntryLocked(TranscriptEntry{
			Kind:     transcriptEntryBlock,
			Title:    "Run",
			Details:  []string{fmt.Sprintf("%s %s", r.agentActionTag(string(action.Type)), label)},
			ActionID: action.ID,
		})
		_ = r.drawLocked()
		r.mu.Unlock()
		return
	}
	r.setActionStatus(firstNonEmpty(r.activePhase, "Running "+label+"..."))
	details := actionProgressDetails(action, r.meta.Provider)
	r.mu.Lock()
	r.appendEntryLocked(TranscriptEntry{
		Kind:     transcriptEntryAction,
		Title:    actionTimelineTitle(action),
		Summary:  action.Reason,
		Details:  details,
		Expanded: len(details) > 0 && len(details) <= 4,
		Status:   string(core.NarrativeRunning),
		ActionID: action.ID,
	})
	_ = r.drawLocked()
	r.mu.Unlock()
}

func actionProgressDetails(action core.Action, provider string) []string {
	if action.Type != core.ActionWebSearch {
		return nil
	}
	query := strings.TrimSpace(firstNonEmpty(action.Query, action.Pattern, action.Title))
	if query == "" {
		return nil
	}
	details := []string{
		"provider: " + firstNonEmpty(strings.TrimSpace(provider), "local"),
		"query: " + query,
	}
	if sites := extractSearchSites(query); len(sites) > 0 {
		details = append(details, "site filters: "+strings.Join(sites, ", "))
	}
	return details
}

func extractSearchSites(query string) []string {
	fields := strings.Fields(query)
	sites := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		if !strings.HasPrefix(strings.ToLower(field), "site:") {
			continue
		}
		site := strings.TrimSpace(strings.TrimPrefix(field, "site:"))
		site = strings.Trim(site, "\"'(),")
		if site == "" {
			continue
		}
		if _, ok := seen[site]; ok {
			continue
		}
		seen[site] = struct{}{}
		sites = append(sites, site)
	}
	return sites
}

func (r *AgentRenderer) StreamActionOutput(action core.Action, chunk core.ActionOutputChunk) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.appendActionStreamLocked(action, chunk) {
		return
	}
	_ = r.drawLocked()
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
	r.setActionStatus("")
	r.mu.Lock()
	if r.mergeResultIntoEntryLocked(result) {
		_ = r.drawLocked()
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	if !r.meta.NarratedProgress {
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
		r.writeBlock("Result", lines)
		return
	}
	details, expanded := resultDetailLines(r, result)
	summary := firstNonEmpty(result.Summary, result.Action.Title)
	r.mu.Lock()
	r.appendEntryLocked(TranscriptEntry{
		Kind:     transcriptEntryResult,
		Summary:  fmt.Sprintf("%s (%s)", summary, status),
		Details:  details,
		Expanded: expanded,
		Status:   status,
		ActionID: result.Action.ID,
	})
	_ = r.drawLocked()
	r.mu.Unlock()
}

func (r *AgentRenderer) Narrate(event core.NarrativeEvent) {
	if !r.meta.NarratedProgress {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendNarrativeLocked(event)
	_ = r.drawLocked()
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

func (r *AgentRenderer) SetModelContext(provider, model string, options []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta.Provider = provider
	r.meta.Model = model
	r.meta.ModelOptions = append([]string(nil), options...)
	_ = r.drawLocked()
}

func (r *AgentRenderer) SetOpsSummary(summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta.OpsSummary = strings.TrimSpace(summary)
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
	r.entries = nil
	r.transcript = nil
	r.scrollOffset = 0
	r.resetComposerLocked()
	r.overlay = overlayNone
	r.overlayIndex = 0
	r.blocker = nil
	r.stopActionStatusLocked()
	r.stopThinkingLocked()
	r.lastFrame = nil
	r.lastWidth = 0
	r.activePhase = ""
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

func (r *AgentRenderer) PromptApprovalChoice(current core.RiskLevel) (core.ApprovalDecision, error) {
	if !current.Valid() {
		current = core.RiskMedium
	}
	if inFile, ok := r.inFile(); ok && term.IsTerminal(int(inFile.Fd())) {
		return r.readApprovalDecisionRaw(inFile, current)
	}
	return r.readApprovalDecisionBuffered(current)
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

func (r *AgentRenderer) readApprovalDecisionBuffered(current core.RiskLevel) (core.ApprovalDecision, error) {
	options := approvalDecisionOptions(current)
	lines := make([]string, 0, len(options))
	for idx, option := range options {
		lines = append(lines, fmt.Sprintf("%d. %s", idx+1, option))
	}
	r.writeBlock("Choose", lines)
	line, err := r.readPromptBuffered("> ")
	if err != nil {
		return core.ApprovalDecision{}, err
	}
	return parseApprovalChoice(line, current), nil
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
	stopResize := r.watchResize()
	defer stopResize()

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

func (r *AgentRenderer) readApprovalDecisionRaw(inFile *os.File, current core.RiskLevel) (core.ApprovalDecision, error) {
	oldState, err := term.MakeRaw(int(inFile.Fd()))
	if err != nil {
		return core.ApprovalDecision{}, err
	}
	defer term.Restore(int(inFile.Fd()), oldState)
	stopResize := r.watchResize()
	defer stopResize()

	r.mu.Lock()
	r.lastPrompt = "> "
	r.overlay = overlayApprovalDecision
	r.overlayIndex = 0
	r.blocker = nil
	r.approvalDecision = &approvalDecisionState{selected: 0, level: current}
	r.ensureAltScreenLocked()
	_ = r.drawLocked()
	r.mu.Unlock()

	for {
		key, err := r.readKeyFn()
		if err != nil {
			return core.ApprovalDecision{}, err
		}
		_, done := r.handleKey(key)
		if !done {
			continue
		}
		r.mu.Lock()
		result := r.approvalDecision.result
		r.approvalDecision = nil
		r.overlay = overlayNone
		_ = r.drawLocked()
		r.mu.Unlock()
		return result, nil
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
	stopResize := r.watchResize()
	defer stopResize()

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

func (r *AgentRenderer) watchResize() func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-signals:
				r.mu.Lock()
				_ = r.drawLocked()
				r.mu.Unlock()
			}
		}
	}()
	return func() {
		close(done)
		signal.Stop(signals)
	}
}

func (r *AgentRenderer) handleKey(key keyPress) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.overlay == overlayBlocker && r.blocker != nil {
		return r.handleBlockerKeyLocked(key)
	}
	if r.overlay == overlayApprovalDecision && r.approvalDecision != nil {
		return r.handleApprovalDecisionKeyLocked(key)
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
		case overlayHistory:
			r.moveOverlayLocked(-1)
		case overlayMode:
			r.moveOverlayLocked(-1)
		case overlayApproval:
			r.moveOverlayLocked(-1)
		case overlayModel:
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
		case overlayHistory:
			r.moveOverlayLocked(1)
		case overlayMode:
			r.moveOverlayLocked(1)
		case overlayApproval:
			r.moveOverlayLocked(1)
		case overlayModel:
			r.moveOverlayLocked(1)
		case overlayPolicy:
			r.moveOverlayLocked(1)
		case overlaySlash:
			r.moveOverlayLocked(1)
		default:
			r.scrollLocked(-1)
		}
	case keyPageUp:
		r.scrollLocked(1)
	case keyPageDown:
		r.scrollLocked(-1)
	case keyTab:
		switch r.overlay {
		case overlayHistory:
			r.moveOverlayLocked(1)
		case overlayPolicy:
			if r.overlayIndex == 0 {
				r.cyclePolicyAutoAcceptLocked(1)
			}
		case overlayNone:
			r.openHistoryOverlayLocked()
		default:
			if r.meta.NarratedProgress && len(r.composer) == 0 && r.overlay == overlayNone && r.toggleLatestExpandableLocked() {
				_ = r.drawLocked()
				return "", false
			}
		}
	case keyShiftTab:
		switch r.overlay {
		case overlayHistory:
			r.moveOverlayLocked(-1)
		case overlayPolicy:
			if r.overlayIndex == 0 {
				r.cyclePolicyAutoAcceptLocked(-1)
			}
		default:
			if r.meta.NarratedProgress && len(r.composer) == 0 && r.overlay == overlayNone && r.toggleLatestExpandableLocked() {
				_ = r.drawLocked()
				return "", false
			}
		}
	case keyEnter:
		switch r.overlay {
		case overlayHistory:
			prompt := r.selectedHistoryPromptLocked()
			r.overlay = overlayNone
			if prompt != "" {
				r.composer = []rune(prompt)
				r.cursor = len(r.composer)
			}
			_ = r.drawLocked()
			return "", false
		case overlayMode:
			command := r.selectedModeCommandLocked()
			r.overlay = overlayNone
			r.recordPromptHistoryLocked(command)
			r.writeUserInputLocked(command)
			_ = r.drawLocked()
			return command, true
		case overlayApproval:
			command := r.selectedApprovalCommandLocked()
			r.overlay = overlayNone
			r.recordPromptHistoryLocked(command)
			r.writeUserInputLocked(command)
			_ = r.drawLocked()
			return command, true
		case overlayModel:
			command := r.selectedModelCommandLocked()
			if command != "" {
				r.overlay = overlayNone
				r.recordPromptHistoryLocked(command)
				r.writeUserInputLocked(command)
				_ = r.drawLocked()
				return command, true
			}
			r.overlay = overlayNone
			_ = r.drawLocked()
			return "", false
		case overlayPolicy:
			r.toggleSelectedPolicyEntryLocked()
			_ = r.drawLocked()
			return "", false
		case overlaySlash:
			command := r.selectedSlashCommandLocked()
			if command == "/approval" {
				r.openApprovalOverlayLocked()
				_ = r.drawLocked()
				return "", false
			}
			if command == "/model" {
				r.openModelOverlayLocked()
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
				r.recordPromptHistoryLocked(command)
				r.writeUserInputLocked(command)
				_ = r.drawLocked()
				return command, true
			}
		default:
			prompt := strings.TrimSpace(string(r.composer))
			r.resetComposerLocked()
			r.overlay = overlayNone
			if prompt != "" {
				r.recordPromptHistoryLocked(prompt)
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

func (r *AgentRenderer) handleApprovalDecisionKeyLocked(key keyPress) (string, bool) {
	if r.approvalDecision == nil {
		return "", false
	}
	switch key.Kind {
	case keyUp:
		r.approvalDecision.selected = (r.approvalDecision.selected + 4) % 5
	case keyDown:
		r.approvalDecision.selected = (r.approvalDecision.selected + 1) % 5
	case keyTab:
		if r.approvalDecision.selected == 2 {
			r.approvalDecision.level = cycleRiskLevel(r.approvalDecision.level, 1)
		}
	case keyShiftTab:
		if r.approvalDecision.selected == 2 {
			r.approvalDecision.level = cycleRiskLevel(r.approvalDecision.level, -1)
		}
	case keyEscape:
		r.approvalDecision.result = core.ApprovalDecision{Kind: core.ApprovalDecisionCancel}
		r.approvalDecision.done = true
	case keyEnter:
		switch r.approvalDecision.selected {
		case 0:
			r.approvalDecision.result = core.ApprovalDecision{Kind: core.ApprovalDecisionAllowOnce}
		case 1:
			r.approvalDecision.result = core.ApprovalDecision{Kind: core.ApprovalDecisionAllowAlways}
		case 2:
			r.approvalDecision.result = core.ApprovalDecision{Kind: core.ApprovalDecisionAutoAccept, AutoAcceptThrough: r.approvalDecision.level}
		case 3:
			r.approvalDecision.result = core.ApprovalDecision{Kind: core.ApprovalDecisionDenyOnce}
		default:
			r.approvalDecision.result = core.ApprovalDecision{Kind: core.ApprovalDecisionCancel}
		}
		r.approvalDecision.done = true
	}
	r.overlayIndex = r.approvalDecision.selected
	_ = r.drawLocked()
	return "", r.approvalDecision.done
}

func (r *AgentRenderer) handleBlockerKeyLocked(key keyPress) (string, bool) {
	if r.blocker == nil {
		return "", false
	}
	switch key.Kind {
	case keyPrintable:
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			r.blocker.followUp = sanitizeComposerRunes(append(r.blocker.followUp, key.Rune))
			r.blocker.focus = blockerFocusFollowUpInput
		} else {
			r.blocker.answer = sanitizeComposerRunes(append(r.blocker.answer, key.Rune))
			r.blocker.focus = blockerFocusInput
		}
	case keyPaste:
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			r.blocker.followUp = sanitizeComposerRunes(append(r.blocker.followUp, []rune(key.Text)...))
			r.blocker.focus = blockerFocusFollowUpInput
		} else {
			r.blocker.answer = sanitizeComposerRunes(append(r.blocker.answer, []rune(key.Text)...))
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
	if ch < 32 || ch == 127 {
		return
	}
	r.composer = sanitizeComposerRunes(append(r.composer, ch))
	r.cursor = len(r.composer)
}

func (r *AgentRenderer) insertTextLocked(text string) {
	text = sanitizeComposerText(text)
	if text == "" {
		return
	}
	r.composer = append(r.composer, []rune(text)...)
	r.cursor = len(r.composer)
}

func sanitizeComposerRunes(value []rune) []rune {
	if len(value) == 0 {
		return nil
	}
	return []rune(sanitizeComposerText(string(value)))
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

func (r *AgentRenderer) openHistoryOverlayLocked() {
	r.overlay = overlayHistory
	r.overlayIndex = 0
}

func (r *AgentRenderer) openApprovalOverlayLocked() {
	r.overlay = overlayApproval
	r.overlayIndex = r.approvalIndexLocked()
}

func (r *AgentRenderer) openModelOverlayLocked() {
	r.overlay = overlayModel
	r.overlayIndex = r.modelIndexLocked()
}

func (r *AgentRenderer) openPolicyOverlayLocked() {
	r.overlay = overlayPolicy
	r.overlayIndex = 0
	r.clampPolicyIndexLocked()
}

func (r *AgentRenderer) updateOverlayLocked() {
	composer := string(r.composer)
	if r.overlay == overlayHistory || r.overlay == overlayMode || r.overlay == overlayApproval || r.overlay == overlayApprovalDecision || r.overlay == overlayModel || r.overlay == overlayPolicy || r.overlay == overlayBlocker {
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
	case overlayHistory:
		size = len(r.promptHistory)
	case overlayMode:
		size = 2
	case overlayApproval:
		size = len(approvalModes)
	case overlayApprovalDecision:
		size = 5
	case overlayModel:
		size = len(r.meta.ModelOptions)
	case overlayPolicy:
		size = len(r.policyRulesLocked()) + 1
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

func (r *AgentRenderer) selectedHistoryPromptLocked() string {
	if len(r.promptHistory) == 0 {
		return ""
	}
	if r.overlayIndex < 0 || r.overlayIndex >= len(r.promptHistory) {
		r.overlayIndex = 0
	}
	idx := len(r.promptHistory) - 1 - r.overlayIndex
	if idx < 0 || idx >= len(r.promptHistory) {
		return ""
	}
	return r.promptHistory[idx]
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

func (r *AgentRenderer) modelIndexLocked() int {
	for idx, model := range r.meta.ModelOptions {
		if model == r.meta.Model {
			return idx
		}
	}
	return 0
}

func (r *AgentRenderer) selectedModelCommandLocked() string {
	if len(r.meta.ModelOptions) == 0 {
		return ""
	}
	if r.overlayIndex < 0 || r.overlayIndex >= len(r.meta.ModelOptions) {
		r.overlayIndex = 0
	}
	return "/model " + r.meta.ModelOptions[r.overlayIndex]
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
		if strings.HasPrefix(left, "secret:") != strings.HasPrefix(right, "secret:") {
			return strings.HasPrefix(left, "secret:")
		}
		if left == "exit" || right == "exit" {
			return right == "exit"
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
	r.appendEntryLocked(TranscriptEntry{
		Kind:    transcriptEntryUser,
		Details: lines,
	})
}

func (r *AgentRenderer) recordPromptHistoryLocked(prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		r.historyIndex = 0
		r.historyDraft = nil
		return
	}
	if len(r.promptHistory) == 0 || r.promptHistory[len(r.promptHistory)-1] != prompt {
		r.promptHistory = append(r.promptHistory, prompt)
	}
	r.historyIndex = 0
	r.historyDraft = nil
}

func (r *AgentRenderer) resetComposerLocked() {
	r.composer = nil
	r.cursor = 0
}

func (r *AgentRenderer) movePromptHistoryLocked(delta int) {
	if len(r.promptHistory) == 0 {
		return
	}
	if delta < 0 {
		if r.historyIndex == 0 {
			r.historyDraft = append([]rune(nil), r.composer...)
		}
		if r.historyIndex < len(r.promptHistory) {
			r.historyIndex++
		}
	} else if delta > 0 {
		if r.historyIndex == 0 {
			return
		}
		r.historyIndex--
	}

	if r.historyIndex == 0 {
		r.composer = append([]rune(nil), r.historyDraft...)
		r.cursor = len(r.composer)
		return
	}

	idx := len(r.promptHistory) - r.historyIndex
	if idx < 0 {
		idx = 0
	}
	if idx >= len(r.promptHistory) {
		idx = len(r.promptHistory) - 1
	}
	r.composer = []rune(r.promptHistory[idx])
	r.cursor = len(r.composer)
}

func (r *AgentRenderer) historyOverlayLinesLocked() []string {
	if len(r.promptHistory) == 0 {
		return []string{"History", "  no prompts yet"}
	}
	lines := []string{"History"}
	for idx := len(r.promptHistory) - 1; idx >= 0; idx-- {
		lines = append(lines, r.overlayOptionLocked(len(r.promptHistory)-1-idx, r.promptHistory[idx], ""))
	}
	lines = append(lines, "  enter recalls selected prompt")
	return lines
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
	if status == "" {
		r.stopActionStatusLocked()
	} else {
		r.actionStatus = status
		r.actionFrame = 0
		if r.actionStop != nil {
			close(r.actionStop)
		}
		stop := make(chan struct{})
		r.actionStop = stop
		go func() {
			ticker := time.NewTicker(r.tickInterval())
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					r.mu.Lock()
					if r.actionStatus == "" || r.actionStop != stop {
						r.mu.Unlock()
						return
					}
					r.actionFrame++
					_ = r.drawLocked()
					r.mu.Unlock()
				case <-stop:
					return
				}
			}
		}()
	}
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

func (r *AgentRenderer) stopActionStatusLocked() {
	r.actionStatus = ""
	r.actionFrame = 0
	if r.actionStop != nil {
		close(r.actionStop)
		r.actionStop = nil
	}
}

func (r *AgentRenderer) writeBlock(title string, lines []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(lines) == 0 {
		lines = []string{""}
	}
	r.appendEntryLocked(TranscriptEntry{
		Kind:    transcriptEntryBlock,
		Title:   title,
		Details: lines,
	})
	_ = r.drawLocked()
}

func (r *AgentRenderer) drawLocked() error {
	width, height := r.sizeFn()
	header := r.headerLines(width, height)
	promptLines := r.promptLinesLocked(width)
	bodyHeight := height - len(header) - len(promptLines) - 3
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
	if r.shouldShowWelcomeLocked() {
		body = r.welcomeLinesLocked(width, bodyHeight)
	}

	r.ensureAltScreenLocked()
	frame := make([]string, 0, height)
	for _, line := range header {
		frame = append(frame, truncateDisplay(line, width))
	}
	if len(body) < bodyHeight {
		for i := 0; i < bodyHeight-len(body); i++ {
			frame = append(frame, "")
		}
	}
	for _, line := range body {
		frame = append(frame, truncateDisplay(line, width))
	}
	for _, line := range overlayLines {
		frame = append(frame, truncateDisplay(line, width))
	}
	frame = append(frame,
		truncateDisplay(r.statusLineLocked(width), width),
	)
	for _, line := range promptLines {
		frame = append(frame, truncateDisplay(line, width))
	}
	frame = append(frame,
		truncateDisplay(r.inputBorderLineLocked(width), width),
		truncateDisplay(r.footerLineLocked(width), width),
	)

	if len(frame) < height {
		for len(frame) < height {
			frame = append(frame, "")
		}
	}
	if len(frame) > height {
		frame = frame[:height]
	}
	cursorRow, cursorCol := r.promptCursorPositionLocked(width, len(header), bodyHeight, len(overlayLines))
	return r.renderFrameLocked(frame, width, cursorRow, cursorCol)
}

func (r *AgentRenderer) headerLines(width, height int) []string {
	line := " SSH-first ai agent. Tab opens prompt history. Type / for command menu. "
	if strings.TrimSpace(r.meta.OpsSummary) != "" {
		line += " | " + r.meta.OpsSummary + " "
	}
	return []string{truncateDisplay(line, width)}
}

func (r *AgentRenderer) shouldShowWelcomeLocked() bool {
	if r.overlay != overlayNone {
		return false
	}
	if len(r.entries) > 0 || len(r.transcript) > 0 {
		return false
	}
	return strings.TrimSpace(string(r.composer)) == ""
}

func (r *AgentRenderer) welcomeLinesLocked(width, bodyHeight int) []string {
	title := "Welcome!"
	if r.meta.WelcomeBack {
		title = "Welcome back!"
	}
	cardWidth := clamp(minInt(width-6, 88), 44, max(44, width-2))
	innerWidth := max(28, cardWidth-2)
	titleLabel := fmt.Sprintf(" IronLark v%s ", buildinfo.NormalizeVersion(buildinfo.Version))
	frameColor := ""
	if r.Color {
		frameColor = ansiGreen
	}
	lines := []string{
		title,
		"",
	}
	lines = append(lines, r.larkMarkLines()...)
	lines = append(lines, "", firstNonEmpty(r.meta.Model, "model unavailable"), compactHomePath(r.meta.CWD))
	box := make([]string, 0, len(lines)+2)
	box = append(box, r.centerLine(colorize(topBorderWithTitle(innerWidth, titleLabel), frameColor), width))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			box = append(box, r.centerLine(colorize("│", frameColor)+strings.Repeat(" ", innerWidth)+colorize("│", frameColor), width))
			continue
		}
		for _, wrapped := range r.wrapWithWidth(line, innerWidth-6) {
			box = append(box, r.centerLine(colorize("│", frameColor)+centerInside(wrapped, innerWidth)+colorize("│", frameColor), width))
		}
	}
	box = append(box, r.centerLine(colorize("└"+strings.Repeat("─", innerWidth)+"┘", frameColor), width))
	out := make([]string, 0, len(box))
	out = append(out, box...)
	if len(out) > bodyHeight {
		return out[len(out)-bodyHeight:]
	}
	return out
}

func (r *AgentRenderer) centerLine(value string, width int) string {
	visible := visibleWidth(value)
	if visible >= width {
		return value
	}
	pad := (width - visible) / 2
	return strings.Repeat(" ", pad) + value
}

func centerInside(value string, width int) string {
	visible := visibleWidth(value)
	if visible >= width {
		return truncateDisplay(value, width)
	}
	left := (width - visible) / 2
	right := width - visible - left
	return strings.Repeat(" ", left) + value + strings.Repeat(" ", right)
}

func topBorderWithTitle(innerWidth int, title string) string {
	titleWidth := visibleWidth(title)
	if innerWidth <= titleWidth+2 {
		return "┌" + strings.Repeat("─", max(0, innerWidth)) + "┐"
	}
	left := 2
	right := innerWidth - titleWidth - left
	if right < 1 {
		right = 1
	}
	return "┌" + strings.Repeat("─", left) + title + strings.Repeat("─", right) + "┐"
}

func (r *AgentRenderer) larkMark() string {
	mark := []string{
		"▟██▙  ▟██▙",
		"██  ███  ██",
		"██  ▀▀  ▄██",
		" ▀██▄▄██▀",
	}
	if !r.Color {
		return strings.Join(mark, "\n")
	}
	painted := make([]string, 0, len(mark))
	for _, line := range mark {
		painted = append(painted, ansiBold+ansiYellow+line+ansiReset)
	}
	return strings.Join(painted, "\n")
}

func (r *AgentRenderer) larkMarkLines() []string {
	return splitMessageLines(r.larkMark())
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (r *AgentRenderer) statusLineLocked(width int) string {
	base := strings.Repeat("-", clamp(width, 12, width))
	switch {
	case r.thinking:
		label := thinkingFrames[r.thinkingFrame%len(thinkingFrames)] + " " + r.thinkingLabel + "  Ctrl+C to stop"
		return truncate(statusWithLabel(width, label), width)
	case r.actionStatus != "":
		label := thinkingFrames[r.actionFrame%len(thinkingFrames)] + " " + r.actionStatus
		return truncate(statusWithLabel(width, label), width)
	default:
		return base
	}
}

func (r *AgentRenderer) promptLinesLocked(width int) []string {
	if r.overlay == overlayBlocker && r.blocker != nil {
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			return r.wrapPromptTailLocked("clarify> ", string(r.blocker.followUp), width, 3)
		}
		value := string(r.blocker.answer)
		if r.blocker.action.InputKind == core.InputSecret && !r.secretVisible {
			value = strings.Repeat("*", len(r.blocker.answer))
		}
		return r.wrapPromptTailLocked("> ", value, width, 3)
	}
	return r.wrapPromptTailLocked("> ", string(r.composer), width, 3)
}

func (r *AgentRenderer) wrapPromptTailLocked(prefix, value string, width, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	value = renderPromptValue(value)
	if width <= 0 {
		return []string{prefix + value}
	}
	continuationPrefix := strings.Repeat(" ", visibleWidth(prefix))
	firstWidth := max(1, width-visibleWidth(prefix))
	nextWidth := max(1, width-visibleWidth(continuationPrefix))
	segments := wrapVisibleTail(value, firstWidth, nextWidth)
	if len(segments) > maxLines {
		segments = segments[len(segments)-maxLines:]
	}
	lines := make([]string, 0, len(segments))
	for idx, segment := range segments {
		linePrefix := continuationPrefix
		if idx == 0 {
			linePrefix = prefix
		}
		lines = append(lines, linePrefix+segment)
	}
	return lines
}

func (r *AgentRenderer) promptCursorPositionLocked(width, headerHeight, bodyHeight, overlayHeight int) (int, int) {
	const maxPromptLines = 3
	prefix, value, cursor := r.activePromptStateLocked()
	value = renderPromptValue(value)
	beforeCursor := renderPromptValue(sliceRunes(value, 0, cursor))
	if cursor <= 0 {
		beforeCursor = ""
	}
	firstWidth := max(1, width-visibleWidth(prefix))
	continuationPrefix := strings.Repeat(" ", visibleWidth(prefix))
	nextWidth := max(1, width-visibleWidth(continuationPrefix))
	segments := wrapVisibleTail(beforeCursor, firstWidth, nextWidth)
	if len(segments) > maxPromptLines {
		segments = segments[len(segments)-maxPromptLines:]
	}
	row := headerHeight + bodyHeight + overlayHeight + 2 + len(segments) - 1
	colPrefix := visibleWidth(prefix)
	if len(segments) > 1 {
		colPrefix = visibleWidth(continuationPrefix)
	}
	col := colPrefix + visibleWidth(segments[len(segments)-1]) + 1
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	return row, col
}

func (r *AgentRenderer) activePromptStateLocked() (string, string, int) {
	if r.overlay == overlayBlocker && r.blocker != nil {
		if r.blocker.editingFollow || r.blocker.focus == blockerFocusFollowUpInput {
			value := string(r.blocker.followUp)
			return "clarify> ", value, utf8.RuneCountInString(value)
		}
		value := string(r.blocker.answer)
		if r.blocker.action.InputKind == core.InputSecret && !r.secretVisible {
			return "> ", strings.Repeat("*", len(r.blocker.answer)), len(r.blocker.answer)
		}
		return "> ", value, utf8.RuneCountInString(value)
	}
	value := string(r.composer)
	cursor := r.cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(r.composer) {
		cursor = len(r.composer)
	}
	return "> ", value, cursor
}

func sliceRunes(value string, start, end int) string {
	runes := []rune(value)
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if start > len(runes) {
		start = len(runes)
	}
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

func (r *AgentRenderer) inputBorderLineLocked(width int) string {
	return strings.Repeat("-", clamp(width, 12, width))
}

func (r *AgentRenderer) footerLineLocked(width int) string {
	left := fmt.Sprintf(" IronLark Agent | approval=%s  mode=%s ", r.meta.ApprovalMode, r.currentModeLabel())
	right := fmt.Sprintf(" \"%s\"  %s ", shortHost(r.meta.Host), r.meta.ThreadID)
	label := joinFooterSides(left, right, width)
	if !r.Color {
		return label
	}
	return ansiBold + ansiBlack + ansiBgGreen + label + ansiReset
}

func (r *AgentRenderer) overlayLinesLocked(width int) []string {
	switch r.overlay {
	case overlayHistory:
		return r.wrapOverlayLines(width, r.historyOverlayLinesLocked())
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
	case overlayApprovalDecision:
		level := core.RiskMedium
		if r.approvalDecision != nil {
			level = r.approvalDecision.level
		}
		lines := []string{"Choose"}
		options := approvalDecisionOptions(level)
		for idx, option := range options {
			lines = append(lines, r.overlayOptionLocked(idx, option, ""))
		}
		return r.wrapOverlayLines(width, lines)
	case overlayModel:
		if len(r.meta.ModelOptions) == 0 {
			return r.wrapOverlayLines(width, []string{"Models", "  no models configured for the active provider"})
		}
		lines := []string{fmt.Sprintf("Models (%s)", firstNonEmpty(r.meta.Provider, "default"))}
		for idx, model := range r.meta.ModelOptions {
			if model == r.meta.Model {
				model += " *"
			}
			lines = append(lines, r.overlayOptionLocked(idx, model, ""))
		}
		return r.wrapOverlayLines(width, lines)
	case overlayPolicy:
		rules := r.policyRulesLocked()
		lines := []string{"Policy", r.overlayOptionLocked(0, r.policyAutoAcceptLabelLocked(), "enter/tab cycles")}
		if len(rules) == 0 {
			lines = append(lines, "  no machine rules yet")
			return r.wrapOverlayLines(width, lines)
		}
		for idx, rule := range rules {
			lines = append(lines, r.overlayOptionLocked(idx+1, formatPolicyRule(rule), ""))
		}
		lines = append(lines, "  enter toggles allow/deny or cycles auto-accept")
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

func (r *AgentRenderer) policyAutoAcceptLabelLocked() string {
	level, ok, err := r.meta.PolicyStore.AutoAcceptThrough()
	if err != nil || !ok {
		return "auto-accept <= OFF"
	}
	return fmt.Sprintf("auto-accept <= %s", level)
}

func (r *AgentRenderer) clampPolicyIndexLocked() {
	size := len(r.policyRulesLocked()) + 1
	if r.overlayIndex < 0 {
		r.overlayIndex = 0
	}
	if r.overlayIndex >= size {
		r.overlayIndex = size - 1
	}
}

func (r *AgentRenderer) toggleSelectedPolicyEntryLocked() {
	if r.overlayIndex == 0 {
		r.cyclePolicyAutoAcceptLocked(1)
		return
	}
	rules := r.policyRulesLocked()
	if len(rules) == 0 {
		return
	}
	r.clampPolicyIndexLocked()
	selected := rules[r.overlayIndex-1]
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

func (r *AgentRenderer) cyclePolicyAutoAcceptLocked(delta int) {
	level, ok, err := r.meta.PolicyStore.AutoAcceptThrough()
	if err != nil {
		return
	}
	next := cycleOptionalRiskLevel(level, ok, delta)
	if next == "" {
		_ = r.meta.PolicyStore.ClearAutoAcceptThrough()
		return
	}
	_ = r.meta.PolicyStore.SetAutoAcceptThrough(next)
}

func (r *AgentRenderer) wrapWithWidth(line string, width int) []string {
	if width <= 0 || visibleWidth(line) <= width {
		return []string{line}
	}
	out := []string{}
	for visibleWidth(line) > width {
		head, rest := splitVisiblePrefix(line, width)
		if head == "" {
			break
		}
		out = append(out, head)
		line = rest
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func wrapVisibleTail(value string, firstWidth, continuationWidth int) []string {
	if firstWidth <= 0 {
		firstWidth = 1
	}
	if continuationWidth <= 0 {
		continuationWidth = firstWidth
	}
	if value == "" {
		return []string{""}
	}
	segments := []string{}
	width := firstWidth
	for {
		if visibleWidth(value) <= width {
			segments = append([]string{value}, segments...)
			break
		}
		keep := visibleTail(value, width)
		segments = append([]string{keep}, segments...)
		value = visibleTrimSuffix(value, width)
		width = continuationWidth
		if value == "" {
			break
		}
	}
	if len(segments) == 0 {
		return []string{""}
	}
	return segments
}

func visibleTail(value string, width int) string {
	if width <= 0 || value == "" {
		return ""
	}
	total := visibleWidth(value)
	if total <= width {
		return value
	}
	return visibleDropPrefix(value, total-width)
}

func visibleTrimSuffix(value string, width int) string {
	total := visibleWidth(value)
	if width <= 0 || total <= width {
		return ""
	}
	return truncateVisible(value, total-width)
}

func visibleDropPrefix(value string, width int) string {
	if width <= 0 {
		return value
	}
	trimmed := value
	remaining := width
	for i := 0; i < len(trimmed) && remaining > 0; {
		if trimmed[i] == 27 {
			end := ansiSequenceEnd(trimmed, i)
			if end <= i {
				trimmed = trimmed[i+1:]
				i = 0
				continue
			}
			trimmed = trimmed[end:]
			i = 0
			continue
		}
		_, size := utf8.DecodeRuneInString(trimmed[i:])
		if size <= 0 {
			size = 1
		}
		trimmed = trimmed[i+size:]
		remaining--
		i = 0
	}
	return trimmed
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

func (r *AgentRenderer) renderFrameLocked(frame []string, width, cursorRow, cursorCol int) error {
	fullRedraw := len(r.lastFrame) != len(frame) || r.lastWidth != width || r.lastFrame == nil
	if fullRedraw {
		if _, err := fmt.Fprint(r.Out, "\033[H\033[2J"); err != nil {
			return err
		}
		for idx, line := range frame {
			if err := r.writeFrameLine(idx+1, line); err != nil {
				return err
			}
		}
	} else {
		for idx, line := range frame {
			if line == r.lastFrame[idx] {
				continue
			}
			if err := r.writeFrameLine(idx+1, line); err != nil {
				return err
			}
		}
	}
	r.lastFrame = append(r.lastFrame[:0], frame...)
	r.lastWidth = width
	if _, err := fmt.Fprintf(r.Out, "\033[%d;%dH\033[?25h", cursorRow, cursorCol); err != nil {
		return err
	}
	r.cursorHidden = false
	return nil
}

func (r *AgentRenderer) writeFrameLine(row int, line string) error {
	_, err := fmt.Fprintf(r.Out, "\033[%d;1H%s\033[K", row, line)
	return err
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
			case '5':
				if r.readEscapeSequence("~") {
					return keyPress{Kind: keyPageUp}, nil
				}
			case '6':
				if r.readEscapeSequence("~") {
					return keyPress{Kind: keyPageDown}, nil
				}
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
	value = sanitizeComposerText(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\\n")
}

func sanitizeComposerText(value string) string {
	if value == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(value); {
		if value[i] == 27 {
			if end := ansiSequenceEnd(value, i); end > i {
				i = end
				continue
			}
			i++
			continue
		}
		r := rune(value[i])
		if r == '\n' || r == '\t' || (r >= 32 && r != 127) {
			b.WriteByte(value[i])
		}
		i++
	}
	return stripLeakedControlNotation(b.String())
}

var leakedControlNotationPattern = regexp.MustCompile(`\^\[\[?[0-9;?]*[A-Za-z~]|\^[A-Z]`)

func stripLeakedControlNotation(value string) string {
	if value == "" {
		return ""
	}
	return leakedControlNotationPattern.ReplaceAllString(value, "")
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
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		b.WriteString(value[i : i+size])
		i += size
		visible++
	}
	if strings.Contains(b.String(), "\033[") && !strings.HasSuffix(b.String(), ansiReset) {
		b.WriteString(ansiReset)
	}
	return b.String()
}

func splitVisiblePrefix(value string, width int) (string, string) {
	if width <= 0 {
		return "", value
	}
	var b strings.Builder
	visible := 0
	index := 0
	for index < len(value) {
		if value[index] == 27 {
			end := ansiSequenceEnd(value, index)
			if end <= index {
				index++
				continue
			}
			b.WriteString(value[index:end])
			index = end
			continue
		}
		if visible >= width {
			break
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == utf8.RuneError && size == 1 {
			index++
			continue
		}
		b.WriteString(value[index : index+size])
		index += size
		visible++
	}
	head := b.String()
	if strings.Contains(head, "\033[") && !strings.HasSuffix(head, ansiReset) {
		head += ansiReset
	}
	return head, value[index:]
}

func padVisibleRight(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = truncateDisplay(value, width)
	padding := width - visibleWidth(value)
	if padding <= 0 {
		return value
	}
	return value + strings.Repeat(" ", padding)
}

func joinFooterSides(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	left = truncateDisplay(left, width)
	right = truncateDisplay(right, width)
	leftWidth := visibleWidth(left)
	rightWidth := visibleWidth(right)
	if leftWidth+rightWidth >= width {
		if rightWidth >= width {
			return truncateDisplay(right, width)
		}
		left = truncateDisplay(left, width-rightWidth)
		leftWidth = visibleWidth(left)
	}
	gap := width - leftWidth - rightWidth
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func shortHost(host string) string {
	host = strings.TrimSpace(host)
	if idx := strings.Index(host, "."); idx > 0 {
		return host[:idx]
	}
	return host
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
		_, size := utf8.DecodeRuneInString(value[i:])
		if size <= 0 {
			size = 1
		}
		i += size
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

func (r *AgentRenderer) assistantPrefix() string {
	label := "⏹ "
	if !r.Color {
		return label
	}
	return ansiBold + ansiYellow + label + ansiReset
}

func (r *AgentRenderer) userChip(text string) string {
	label := "› " + text + " "
	if !r.Color {
		return label
	}
	return ansiBlack + ansiBgWhite + label + ansiReset
}

func compactHomePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	home = filepath.Clean(home)
	path = filepath.Clean(path)
	if path == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		return "~" + string(os.PathSeparator) + strings.TrimPrefix(path, prefix)
	}
	return path
}

func distinctFindings(summary string, findings []string) []string {
	summaryNorm := normalizeFindingText(summary)
	out := make([]string, 0, len(findings))
	seen := map[string]struct{}{}
	for _, finding := range findings {
		trimmed := strings.TrimSpace(finding)
		if trimmed == "" {
			continue
		}
		norm := normalizeFindingText(trimmed)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		if summaryNorm != "" && (norm == summaryNorm || strings.Contains(summaryNorm, norm) || strings.Contains(norm, summaryNorm) || tokenOverlapRatio(summaryNorm, norm) >= 0.8) {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeFindingText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSpace = false
		case lastSpace:
			continue
		default:
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func tokenOverlapRatio(left, right string) float64 {
	leftTokens := strings.Fields(left)
	rightTokens := strings.Fields(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	leftSet := map[string]struct{}{}
	for _, token := range leftTokens {
		leftSet[token] = struct{}{}
	}
	overlap := 0
	rightSeen := map[string]struct{}{}
	for _, token := range rightTokens {
		if _, ok := rightSeen[token]; ok {
			continue
		}
		rightSeen[token] = struct{}{}
		if _, ok := leftSet[token]; ok {
			overlap++
		}
	}
	denominator := len(rightSeen)
	if len(leftSet) > denominator {
		denominator = len(leftSet)
	}
	if denominator == 0 {
		return 0
	}
	return float64(overlap) / float64(denominator)
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

func (r *AgentRenderer) streamLine(chunk core.ActionOutputChunk) string {
	lineColor := ansiGray
	if chunk.Stream == core.ActionOutputStderr {
		lineColor = ansiRed
	}
	return "  " + r.agentOutputPrefix(lineColor) + " " + r.agentOutputLine(strings.TrimSpace(chunk.Text), lineColor)
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
