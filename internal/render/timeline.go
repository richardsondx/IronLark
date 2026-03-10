package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

type transcriptEntryKind string

const (
	transcriptEntryBlock     transcriptEntryKind = "block"
	transcriptEntryAssistant transcriptEntryKind = "assistant"
	transcriptEntryNarration transcriptEntryKind = "narration"
	transcriptEntryAction    transcriptEntryKind = "action"
	transcriptEntryResult    transcriptEntryKind = "result"
	transcriptEntryUser      transcriptEntryKind = "user"
)

type TranscriptEntry struct {
	Kind      transcriptEntryKind
	Title     string
	Summary   string
	Details   []string
	Expanded  bool
	Status    string
	Timestamp time.Time
	ActionID  string
	Streamed  bool
}

func (r *AgentRenderer) appendEntryLocked(entry TranscriptEntry) {
	entry.Timestamp = r.nowFn().UTC()
	r.entries = append(r.entries, entry)
	r.transcript = r.renderTranscriptLocked()
}

func (r *AgentRenderer) actionEntryIndexLocked(actionID string) int {
	for idx := len(r.entries) - 1; idx >= 0; idx-- {
		if r.entries[idx].ActionID == actionID {
			return idx
		}
	}
	return -1
}

func (r *AgentRenderer) appendActionStreamLocked(action core.Action, chunk core.ActionOutputChunk) bool {
	idx := r.actionEntryIndexLocked(action.ID)
	if idx < 0 {
		return false
	}
	line := r.streamLine(chunk)
	if strings.TrimSpace(line) == "" {
		return false
	}
	entry := &r.entries[idx]
	entry.Streamed = true
	entry.Expanded = true
	entry.Details = append(entry.Details, line)
	if len(entry.Details) > 80 {
		entry.Details = entry.Details[len(entry.Details)-80:]
	}
	r.transcript = r.renderTranscriptLocked()
	return true
}

func (r *AgentRenderer) mergeResultIntoEntryLocked(result core.ActionResult) bool {
	idx := r.actionEntryIndexLocked(result.Action.ID)
	if idx < 0 {
		return false
	}
	entry := &r.entries[idx]
	status := "ok"
	if result.Error != "" {
		status = "error"
	}
	if result.Skipped {
		status = "skipped"
	}
	includeStreams := !entry.Streamed
	switch entry.Kind {
	case transcriptEntryAction:
		entry.Status = status
		entry.Summary = firstNonEmpty(result.Summary, entry.Summary, result.Action.Title)
		entry.Details = append(entry.Details, resultExtraDetailLines(r, result, includeStreams)...)
		if len(entry.Details) <= 6 {
			entry.Expanded = true
		}
	case transcriptEntryBlock:
		if result.Summary != "" {
			entry.Details = append(entry.Details, r.agentLabel("Summary")+": "+result.Summary)
		}
		entry.Details = append(entry.Details, resultExtraDetailLines(r, result, includeStreams)...)
	default:
		return false
	}
	r.transcript = r.renderTranscriptLocked()
	return true
}

func (r *AgentRenderer) renderTranscriptLocked() []string {
	lines := make([]string, 0, len(r.entries)*3)
	for _, entry := range r.entries {
		lines = append(lines, r.renderEntryLinesLocked(entry)...)
	}
	return lines
}

func (r *AgentRenderer) renderEntryLinesLocked(entry TranscriptEntry) []string {
	switch entry.Kind {
	case transcriptEntryAssistant:
		return r.renderAssistantEntryLines(entry)
	case transcriptEntryNarration:
		return append([]string{""}, r.wrapTimelineEntry("o", entry.Summary, "", 0)...)
	case transcriptEntryAction:
		lines := []string{""}
		lines = append(lines, r.wrapTimelineEntry(r.actionGlyph(entry.Status), entry.Title, ansiGreen, 0)...)
		if entry.Summary != "" {
			for _, wrapped := range r.wrapWithWidth(entry.Summary, max(8, r.bodyWidth()-2)) {
				lines = append(lines, "  "+wrapped)
			}
		}
		return append(lines, r.renderEntryDetails(entry, "  ")...)
	case transcriptEntryResult:
		lines := r.wrapTimelineEntry(r.resultGlyph(entry.Status), entry.Summary, "", 2)
		return append(lines, r.renderEntryDetails(entry, "    ")...)
	case transcriptEntryUser:
		out := make([]string, 0, len(entry.Details)+1)
		for _, line := range entry.Details {
			if strings.TrimSpace(line) == "" {
				continue
			}
			for _, wrapped := range r.wrapWithWidth(line, max(8, r.bodyWidth()-4)) {
				out = append(out, "  "+r.userChip(wrapped))
			}
		}
		return out
	default:
		lines := []string{"", "## " + entry.Title}
		if entry.Summary != "" {
			lines = append(lines, r.wrapWithWidth(entry.Summary, r.bodyWidth())...)
		}
		for _, line := range entry.Details {
			if strings.TrimSpace(line) == "" {
				lines = append(lines, "")
				continue
			}
			lines = append(lines, r.wrapWithWidth(line, r.bodyWidth())...)
		}
		return lines
	}
}

func (r *AgentRenderer) wrapTimelineEntry(glyph, text, color string, indent int) []string {
	prefix := strings.Repeat(" ", max(0, indent)) + r.timelineLine(glyph, "", color)
	prefix = strings.TrimRight(prefix, " ")
	if prefix == "" {
		prefix = strings.Repeat(" ", max(0, indent))
	}
	firstPrefix := prefix + " "
	bodyWidth := max(8, r.bodyWidth()-visibleWidth(firstPrefix))
	wrapped := r.wrapWithWidth(text, bodyWidth)
	if len(wrapped) == 0 {
		return []string{firstPrefix}
	}
	lines := make([]string, 0, len(wrapped))
	continuationPrefix := strings.Repeat(" ", visibleWidth(firstPrefix))
	for idx, line := range wrapped {
		if idx == 0 {
			lines = append(lines, firstPrefix+line)
			continue
		}
		lines = append(lines, continuationPrefix+line)
	}
	return lines
}

func (r *AgentRenderer) renderAssistantEntryLines(entry TranscriptEntry) []string {
	lines := []string{""}
	prefix := r.assistantPrefix()
	bodyWidth := max(8, r.bodyWidth()-visibleWidth(prefix))
	summary := entry.Summary
	if strings.TrimSpace(summary) == "" {
		summary = "I don't have a complete answer yet."
	}
	for idx, wrapped := range r.wrapWithWidth(summary, bodyWidth) {
		if idx == 0 {
			lines = append(lines, prefix+wrapped)
			continue
		}
		lines = append(lines, strings.Repeat(" ", visibleWidth(prefix))+wrapped)
	}
	for _, line := range entry.Details {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, wrapped := range r.wrapWithWidth(line, max(8, r.bodyWidth()-4)) {
			lines = append(lines, "  "+wrapped)
		}
	}
	return lines
}

func (r *AgentRenderer) renderEntryDetails(entry TranscriptEntry, prefix string) []string {
	if len(entry.Details) == 0 {
		return nil
	}
	details := entry.Details
	if !entry.Expanded {
		details = nil
	}
	out := make([]string, 0, len(details)+1)
	if !entry.Expanded {
		out = append(out, prefix+fmt.Sprintf("%d detail line(s) hidden", len(entry.Details)))
		return out
	}
	for _, line := range details {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		for _, wrapped := range r.wrapWithWidth(line, max(8, r.bodyWidth()-len(prefix))) {
			out = append(out, prefix+wrapped)
		}
	}
	return out
}

func (r *AgentRenderer) timelineLine(glyph, text, color string) string {
	if !r.Color || color == "" {
		return glyph + " " + text
	}
	return color + glyph + ansiReset + " " + text
}

func (r *AgentRenderer) actionGlyph(status string) string {
	switch status {
	case string(core.NarrativeError):
		return "x"
	case string(core.NarrativeSkipped):
		return "-"
	default:
		return "+"
	}
}

func (r *AgentRenderer) resultGlyph(status string) string {
	switch status {
	case string(core.NarrativeError):
		return "x"
	case string(core.NarrativeSkipped):
		return "-"
	default:
		return "|"
	}
}

func (r *AgentRenderer) appendNarrativeLocked(event core.NarrativeEvent) {
	phase := strings.TrimSpace(event.Phase)
	if phase != "" {
		r.activePhase = phase
		if r.thinking {
			r.thinkingLabel = phase
		}
		if r.actionStatus != "" {
			r.actionStatus = phase
		}
	}
	r.appendEntryLocked(TranscriptEntry{
		Kind:     transcriptEntryNarration,
		Summary:  event.Text,
		Status:   string(event.Status),
		ActionID: event.ActionID,
	})
}

func (r *AgentRenderer) toggleLatestExpandableLocked() bool {
	for idx := len(r.entries) - 1; idx >= 0; idx-- {
		if len(r.entries[idx].Details) == 0 {
			continue
		}
		r.entries[idx].Expanded = !r.entries[idx].Expanded
		r.transcript = r.renderTranscriptLocked()
		return true
	}
	return false
}

func actionTimelineTitle(action core.Action) string {
	target := firstNonEmpty(action.Path, firstPath(action.Paths), action.Query, action.Command, action.Title)
	switch action.Type {
	case core.ActionReadFiles:
		return fmt.Sprintf("Read(%s)", target)
	case core.ActionListDir:
		return fmt.Sprintf("List(%s)", target)
	case core.ActionSearchFiles:
		return fmt.Sprintf("Search(%s)", firstNonEmpty(action.Pattern, action.Query, target))
	case core.ActionSemanticSearch:
		return fmt.Sprintf("SemanticSearch(%s)", firstNonEmpty(action.Query, action.Pattern, target))
	case core.ActionEditFile:
		return fmt.Sprintf("Update(%s)", target)
	case core.ActionWebSearch:
		return fmt.Sprintf("WebSearch(%s)", target)
	case core.ActionFetchRules:
		return fmt.Sprintf("FetchRules(%s)", target)
	case core.ActionCheckpoint:
		return fmt.Sprintf("Checkpoint(%s)", target)
	case core.ActionAskUser:
		return fmt.Sprintf("Input(%s)", firstNonEmpty(action.Prompt, action.Title, action.FieldKey))
	case core.ActionRunShell:
		return fmt.Sprintf("Run(%s)", firstNonEmpty(action.Command, action.Title))
	default:
		return fmt.Sprintf("%s(%s)", action.Type, target)
	}
}

func resultDetailLines(r *AgentRenderer, result core.ActionResult) ([]string, bool) {
	lines := resultExtraDetailLines(r, result, true)
	return lines, len(lines) <= 6
}

func resultExtraDetailLines(r *AgentRenderer, result core.ActionResult, includeStreams bool) []string {
	lines := []string{}
	if includeStreams && result.Stdout != "" {
		lines = append(lines, r.agentLabel("Output")+":")
		for _, line := range strings.Split(strings.TrimRight(result.Stdout, "\n"), "\n") {
			lines = append(lines, "  "+r.agentOutputPrefix(ansiGray)+" "+r.agentOutputLine(line, ansiGray))
		}
	}
	if includeStreams && result.Stderr != "" {
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
	return lines
}

func firstPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
