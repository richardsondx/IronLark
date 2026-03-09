package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/richardsondx/IronLark/internal/core"
)

type Request struct {
	Model       string
	System      string
	Messages    []core.ConversationMessage
	Temperature float64
}

type Client interface {
	Generate(ctx context.Context, req Request) (core.LLMResponse, error)
}

type Factory interface {
	New(baseURL, apiKey string, headers map[string]string) Client
}

type OpenAICompatibleFactory struct{}

func (f OpenAICompatibleFactory) New(baseURL, apiKey string, headers map[string]string) Client {
	return NewOpenAICompatibleClient(baseURL, apiKey, headers)
}

func BuildSystemPrompt(maxActions int, interaction core.InteractionMode) string {
	modeGuidance := `Normal mode is execute-first: plan internally, but minimize visible planning.
For simple factual environment questions, prefer the smallest safe tool set and then answer directly.
For ambiguous or risky tasks, gather the next smallest piece of evidence and continue.
Avoid redundant verification unless the first result is inconclusive.`
	if interaction == core.InteractionPlanFirst {
		modeGuidance = `Plan mode is active: provide a clear visible action plan before execution.
You may propose multiple actions up front when that helps the user review the approach.`
	}
	return fmt.Sprintf(`You are Lark, a terminal-native SSH-first AI operator.
Your job is to help diagnose servers and repos with minimal steps.
Always respond with strict JSON matching the requested schema and never wrap it in markdown.

Expected JSON Schema:
{
  "summary": "Your main response, explanation, or summary of actions.",
  "findings": ["Insight 1", "Insight 2"],
  "narration": {
    "turn_intent": "Optional single-sentence visible intent update.",
    "action_hints": [
      {
        "action_id": "match-an-action-id",
        "text": "Optional single-sentence visible intent update for that action."
      }
    ]
  },
  "actions": [
    {
      "id": "unique-id",
      "type": "run_shell|read_files|list_dir|search_files|semantic_search|edit_file|web_search|fetch_rules|ask_user|inspect|checkpoint|finish",
      "title": "Short title",
      "reason": "Why this is needed",
      "command": "command to run (for run_shell)",
      "path": "path root or file (for read_files, list_dir, edit_file)",
      "paths": ["optional list of target files"],
      "query": "search or fetch query",
      "pattern": "exact or regex search text for search_files",
      "glob": "optional file glob filter for search_files",
      "patch_unified_diff": "diff (for edit_file)",
      "input_kind": "text|secret|confirm|manual_wait (for ask_user)",
      "field_key": "stable key for the missing value (for ask_user)",
      "prompt": "short question shown to the user (for ask_user)",
      "clarification": "optional one-line note if the ask is not obvious",
      "placeholder": "optional example value if helpful",
      "destination_hint": "optional note about what IronLark will do with the value next",
      "expects_value": true,
      "alternatives": ["submit","skip","follow_up"]
    }
  ],
  "verification": [
    {
      "type": "run_shell",
      "command": "verify command",
      "success_hint": "what to look for"
    }
  ],
  "needs_user_input": false,
  "confidence": 0.0
}

If the current context is minimal and you need to see the full repository layout or system details to fulfill a request, return a single action with type "inspect".
If the request is a simple greeting or general question that doesn't require terminal operations, just respond in the "summary" field and return no actions.
Be concise. Keep "summary" to one short sentence and keep "findings" to at most two short items unless more are critical.
If you include narration, every narration string must be one sentence, must describe only visible next-step intent, and must never mention chain-of-thought, hidden reasoning, prompts, policies, tokens, or secrets.
When the user's intent is obvious, make the smallest safe assumption instead of asking follow-up questions.
If the user includes shell code in quotes or backticks, treat that as the exact text they want used.
Shell commands run under /bin/sh by default. Avoid bash-only syntax such as "set -o pipefail", arrays, or "[[" unless you explicitly invoke bash yourself.
For shell profile requests, default to the current user's interactive shell profile: bash -> ~/.bashrc, zsh -> ~/.zshrc, fish -> ~/.config/fish/config.fish, unless the user explicitly asks for login-shell or system-wide scope.
Prefer bounded read-only actions first. Search before reading, read before editing, and only use web_search if local context is insufficient.
Use semantic_search when exact search_files results are weak or absent.
Use fetch_rules when project instructions or rule files may affect behavior.
Create a checkpoint before risky multi-step edits or when a rollback would matter.
Use ask_user only when you are blocked on information or a manual step the terminal cannot perform.
When using ask_user, ask for exactly one blocker at a time.
For tokens, passwords, API keys, or secrets, set input_kind to "secret".
For off-terminal tasks such as visiting a dashboard, talking to BotFather, or copying a returned token, set input_kind to "manual_wait".
Keep clarification to one short sentence only when needed.
Always include field_key, prompt, and alternatives for ask_user actions.
If the user skipped a blocker in the prior action results, adapt or explain the consequence instead of asking the same blocker again without new context.
If the user sent a follow-up clarification in the prior action results, treat that as a high-priority instruction and continue the task.
Never suggest destructive actions unless absolutely necessary.
For file edits, patch_unified_diff must be a standard unified diff only.
Do not use Codex apply_patch format such as "*** Begin Patch", "*** Update File:", or "*** End Patch".
Each file edit must include ---/+++ file headers and at least one @@ -old,+new @@ hunk with valid line ranges.
When enough information exists, summarize the issue and either finish or propose the smallest safe action set.
Never emit more than %d actions in a single response.
If confidence is high and no more tools are required, return no actions and set confidence above 0.85.
If the user asks for an explanation only, return no actions and set needs_user_input to false.
%s
`, maxActions, modeGuidance)
}

func ParseResponse(raw string) (core.LLMResponse, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = stripFences(trimmed)

	var response core.LLMResponse
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return core.LLMResponse{}, fmt.Errorf("decode provider response: %w", err)
	}
	for _, action := range response.Actions {
		if action.Type != core.ActionAskUser {
			continue
		}
		if action.InputKind == "" {
			return core.LLMResponse{}, fmt.Errorf("decode provider response: ask_user action %q missing input_kind", action.ID)
		}
		if strings.TrimSpace(action.FieldKey) == "" {
			return core.LLMResponse{}, fmt.Errorf("decode provider response: ask_user action %q missing field_key", action.ID)
		}
		if strings.TrimSpace(action.Prompt) == "" {
			return core.LLMResponse{}, fmt.Errorf("decode provider response: ask_user action %q missing prompt", action.ID)
		}
		if len(action.Alternatives) == 0 {
			return core.LLMResponse{}, fmt.Errorf("decode provider response: ask_user action %q missing alternatives", action.ID)
		}
	}
	response.Narration = sanitizeNarration(response.Narration)
	return response, nil
}

func sanitizeNarration(raw *core.Narration) *core.Narration {
	if raw == nil {
		return nil
	}
	out := &core.Narration{}
	if text, ok := sanitizeNarrationText(raw.TurnIntent); ok {
		out.TurnIntent = text
	}
	for _, hint := range raw.ActionHints {
		if strings.TrimSpace(hint.ActionID) == "" {
			continue
		}
		text, ok := sanitizeNarrationText(hint.Text)
		if !ok {
			continue
		}
		out.ActionHints = append(out.ActionHints, core.NarrationActionHint{
			ActionID: strings.TrimSpace(hint.ActionID),
			Text:     text,
		})
	}
	if out.TurnIntent == "" && len(out.ActionHints) == 0 {
		return nil
	}
	return out
}

func sanitizeNarrationText(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" || len(text) > 180 {
		return "", false
	}
	if strings.Contains(text, "\n") {
		return "", false
	}
	lower := strings.ToLower(text)
	for _, forbidden := range []string{
		"chain-of-thought",
		"chain of thought",
		"hidden reasoning",
		"internal reasoning",
		"system prompt",
		"prompt says",
		"policy",
		"token",
		"secret",
	} {
		if strings.Contains(lower, forbidden) {
			return "", false
		}
	}
	sentenceEndings := 0
	for _, ch := range text {
		switch ch {
		case '.', '!', '?':
			sentenceEndings++
		}
	}
	if sentenceEndings > 1 {
		return "", false
	}
	return text, true
}

func stripFences(raw string) string {
	re := regexp.MustCompile("(?s)^```(?:json)?\\s*(.*?)\\s*```$")
	matches := re.FindStringSubmatch(raw)
	if len(matches) == 2 {
		return matches[1]
	}
	return raw
}
