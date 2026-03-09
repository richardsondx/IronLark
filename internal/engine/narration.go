package engine

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

func (e *Engine) narratedProgressEnabled() bool {
	return e.Runtime.Config.UI.NarratedProgress && !e.Runtime.JSONOutput
}

func (e *Engine) narrate(event core.NarrativeEvent) {
	if !e.narratedProgressEnabled() {
		return
	}
	e.Renderer.Narrate(event)
}

type turnNarrator struct {
	seed   int64
	rng    *rand.Rand
	recent []string
	hints  map[string]string
	seq    int
}

func newTurnNarrator(seed int64, narration *core.Narration) *turnNarrator {
	hints := map[string]string{}
	if narration != nil {
		for _, hint := range narration.ActionHints {
			hints[hint.ActionID] = hint.Text
		}
	}
	return &turnNarrator{
		seed:  seed,
		rng:   rand.New(rand.NewSource(seed)),
		hints: hints,
	}
}

func (n *turnNarrator) event(kind core.NarrativeKind, phase, text, actionID, target string, status core.NarrativeStatus, details ...string) core.NarrativeEvent {
	n.seq++
	text = strings.TrimSpace(text)
	n.remember(text)
	return core.NarrativeEvent{
		ID:        fmt.Sprintf("narrative-%d-%d", n.seed, n.seq),
		Kind:      kind,
		Phase:     strings.TrimSpace(phase),
		Text:      text,
		ActionID:  actionID,
		Target:    target,
		Status:    status,
		Details:   details,
		Timestamp: time.Now().UTC(),
	}
}

func (n *turnNarrator) turnStarted() core.NarrativeEvent {
	return n.event(core.NarrativeTurnStarted, "Understanding the task", n.pick([]string{
		"Let me inspect the current context before I commit to a next step.",
		"I’m taking a quick look at the current context before I move.",
		"I want one fast pass over the context before I take the next step.",
	}), "", "", core.NarrativeRunning)
}

func (n *turnNarrator) contextShift() core.NarrativeEvent {
	templates := []string{
		"I need the fuller repo and machine context before I take the next step.",
		"The first pass was too thin, so I’m collecting the broader context now.",
		"I need a wider workspace and system view before I continue.",
	}
	return n.event(core.NarrativeContextShift, "Inspecting more context", n.pick(templates), "", "", core.NarrativeRunning)
}

func (n *turnNarrator) intent(response core.LLMResponse) core.NarrativeEvent {
	if response.Narration != nil && strings.TrimSpace(response.Narration.TurnIntent) != "" {
		return n.event(core.NarrativeIntent, "Framing the next step", response.Narration.TurnIntent, "", targetFromResponse(response), core.NarrativeDone)
	}
	target := targetFromResponse(response)
	text := n.pick([]string{
		fmt.Sprintf("I found a likely starting point in %s, so I’m checking that first.", target),
		fmt.Sprintf("There’s enough signal in %s to make that my next step.", target),
		fmt.Sprintf("The next smallest safe move is to inspect %s.", target),
		fmt.Sprintf("I can make progress from %s, so I’m following that now.", target),
	})
	if target == "" {
		text = n.pick([]string{
			"I have enough context to take the next smallest step now.",
			"I can move from context gathering into the next concrete step.",
			"The next step is clear enough to execute safely.",
		})
	}
	return n.event(core.NarrativeIntent, "Framing the next step", text, firstActionID(response.Actions), target, core.NarrativeDone)
}

func (n *turnNarrator) actionGroup(actions []core.Action) core.NarrativeEvent {
	targets := make([]string, 0, min(2, len(actions)))
	for _, action := range actions {
		target := actionTarget(action)
		if target == "" {
			continue
		}
		targets = append(targets, target)
		if len(targets) == 2 {
			break
		}
	}
	text := "I have a short sequence to work through next."
	if len(targets) > 0 {
		text = fmt.Sprintf("I’m going to work through a short sequence starting with %s.", strings.Join(targets, " then "))
	}
	return n.event(core.NarrativeActionGroup, "Sequencing actions", text, firstActionID(actions), strings.Join(targets, ", "), core.NarrativePending)
}

func (n *turnNarrator) actionStarted(action core.Action) core.NarrativeEvent {
	target := actionTarget(action)
	if hint := strings.TrimSpace(n.hints[action.ID]); hint != "" {
		return n.event(core.NarrativeActionStarted, phaseForAction(action), hint, action.ID, target, core.NarrativeRunning)
	}
	templates := []string{
		fmt.Sprintf("I found %s, so I’m checking that before changing anything.", target),
		fmt.Sprintf("There’s already a useful starting point in %s, so I’m inspecting it first.", target),
		fmt.Sprintf("The last step narrowed this down to %s, so I’m following that next.", target),
		fmt.Sprintf("I can verify the next decision by looking at %s.", target),
	}
	if target == "" {
		templates = []string{
			"I’m taking the next concrete step now.",
			"I have enough signal to run the next action.",
			"I’m moving forward with the next safe action.",
		}
	}
	return n.event(core.NarrativeActionStarted, phaseForAction(action), n.pick(templates), action.ID, target, core.NarrativeRunning)
}

func (n *turnNarrator) actionFinished(result core.ActionResult) core.NarrativeEvent {
	status := core.NarrativeDone
	if result.Error != "" {
		status = core.NarrativeError
	} else if result.Skipped {
		status = core.NarrativeSkipped
	}
	text := result.Summary
	if text == "" {
		text = "That step finished."
	}
	text = n.pick([]string{
		fmt.Sprintf("That finished with: %s.", text),
		fmt.Sprintf("I got what I needed from that step: %s.", text),
		fmt.Sprintf("That result came back as: %s.", text),
	})
	return n.event(core.NarrativeActionFinished, "Reviewing the result", text, result.Action.ID, actionTarget(result.Action), status)
}

func (n *turnNarrator) verificationStarted(action core.Action) core.NarrativeEvent {
	target := actionTarget(action)
	text := n.pick([]string{
		fmt.Sprintf("I’m verifying the change against %s before I wrap up.", target),
		fmt.Sprintf("I want to confirm the result with %s.", target),
		fmt.Sprintf("I’m doing one quick verification pass on %s.", target),
	})
	return n.event(core.NarrativeVerificationStart, "Verifying", text, action.ID, target, core.NarrativeRunning)
}

func (n *turnNarrator) blocked(action core.Action, reason string, status core.NarrativeStatus) core.NarrativeEvent {
	target := actionTarget(action)
	text := strings.TrimSpace(reason)
	if text == "" {
		text = "I’m blocked on the next step."
	}
	if target != "" {
		text = fmt.Sprintf("%s (%s)", text, target)
	}
	return n.event(core.NarrativeBlocked, "Blocked", text, action.ID, target, status)
}

func (n *turnNarrator) interrupted() core.NarrativeEvent {
	return n.event(core.NarrativeTurnFinished, "Interrupted", "The current turn was interrupted. I’m waiting for the next instruction.", "", "", core.NarrativeError)
}

func (n *turnNarrator) turnFinished(summary string) core.NarrativeEvent {
	text := strings.TrimSpace(summary)
	if text == "" {
		text = "This turn is complete."
	}
	return n.event(core.NarrativeTurnFinished, "Done", text, "", "", core.NarrativeDone)
}

func (n *turnNarrator) pick(candidates []string) string {
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if len(n.recent) > 0 && candidate == n.recent[len(n.recent)-1] {
			continue
		}
		filtered = append(filtered, candidate)
	}
	if len(filtered) == 0 {
		filtered = candidates
	}
	return filtered[n.rng.Intn(len(filtered))]
}

func (n *turnNarrator) remember(text string) {
	if text == "" {
		return
	}
	n.recent = append(n.recent, text)
	if len(n.recent) > 4 {
		n.recent = n.recent[len(n.recent)-4:]
	}
}

func firstActionID(actions []core.Action) string {
	if len(actions) == 0 {
		return ""
	}
	return actions[0].ID
}

func targetFromResponse(response core.LLMResponse) string {
	if len(response.Actions) > 0 {
		return actionTarget(response.Actions[0])
	}
	if len(response.Findings) > 0 {
		return response.Findings[0]
	}
	return response.Summary
}

func actionTarget(action core.Action) string {
	return firstNonEmpty(action.Path, firstPath(action.Paths), action.Query, action.Pattern, action.Command, action.Title, action.FieldKey)
}

func phaseForAction(action core.Action) string {
	switch action.Type {
	case core.ActionReadFiles:
		return "Reading files"
	case core.ActionListDir:
		return "Listing files"
	case core.ActionSearchFiles, core.ActionSemanticSearch:
		return "Searching"
	case core.ActionEditFile:
		return "Updating files"
	case core.ActionWebSearch:
		return "Searching the web"
	case core.ActionFetchRules:
		return "Checking project rules"
	case core.ActionCheckpoint:
		return "Saving a checkpoint"
	case core.ActionAskUser:
		return "Waiting for input"
	default:
		return "Running action"
	}
}

func firstPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func responseHasVisibleContent(response core.LLMResponse) bool {
	if strings.TrimSpace(response.Summary) != "" {
		return true
	}
	if len(response.Findings) > 0 || len(response.Actions) > 0 || len(response.Verification) > 0 || response.NeedsUserInput {
		return true
	}
	return false
}

func normalizedResponse(response core.LLMResponse) core.LLMResponse {
	if strings.TrimSpace(response.Summary) != "" {
		return response
	}
	response.Summary = fallbackSummary(response)
	return response
}

func fallbackSummary(response core.LLMResponse) string {
	if len(response.Findings) > 0 && strings.TrimSpace(response.Findings[0]) != "" {
		return strings.TrimSpace(response.Findings[0])
	}
	if len(response.Actions) > 0 {
		action := response.Actions[0]
		switch {
		case strings.TrimSpace(action.Reason) != "":
			return strings.TrimSpace(action.Reason)
		case strings.TrimSpace(action.Title) != "":
			return strings.TrimSpace(action.Title)
		case strings.TrimSpace(action.Command) != "":
			return "Running the next step."
		}
	}
	if len(response.Verification) > 0 {
		return "Verifying the result."
	}
	if response.NeedsUserInput {
		return "I need one more input before I can continue."
	}
	return fallbackSummaryText
}

const fallbackSummaryText = "I wasn't able to produce a complete answer on the first pass, so I need to try again."

func isSyntheticFallbackResponse(response core.LLMResponse) bool {
	if strings.TrimSpace(response.Summary) != fallbackSummaryText {
		return false
	}
	return len(response.Findings) == 0 && len(response.Actions) == 0 && len(response.Verification) == 0 && !response.NeedsUserInput
}

var simpleGreetingPattern = regexp.MustCompile(`(?i)^\s*(hi|hello|hey|yo|sup|good (morning|afternoon|evening)|hello world)\s*[!.?]*\s*$`)

func isSimpleGreetingPrompt(prompt string) bool {
	return simpleGreetingPattern.MatchString(prompt)
}

func greetingSummary(prompt string) string {
	return "Hi! How can I help you with your server or repo today?"
}

func greetingOnlyResponse(prompt string) core.LLMResponse {
	return core.LLMResponse{
		Summary: greetingSummary(prompt),
	}
}

func shouldReplaceWithGreetingSummary(prompt string, response core.LLMResponse) bool {
	if !isSimpleGreetingPrompt(prompt) {
		return false
	}
	if len(response.Actions) > 0 || len(response.Findings) > 0 || len(response.Verification) > 0 || response.NeedsUserInput {
		return false
	}
	summary := strings.TrimSpace(strings.ToLower(response.Summary))
	return summary == "" || strings.Contains(summary, "wasn't able to produce a complete answer")
}
