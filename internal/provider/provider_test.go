package provider

import (
	"strings"
	"testing"
)

func TestParseResponseAcceptsStructuredAskUser(t *testing.T) {
	raw := `{
  "summary": "Need a token",
  "findings": [],
  "actions": [
    {
      "id": "ask-1",
      "type": "ask_user",
      "title": "Request token",
      "reason": "Setup is blocked without it",
      "input_kind": "secret",
      "field_key": "bot_token",
      "prompt": "Paste the bot token",
      "alternatives": ["submit","skip","follow_up"]
    }
  ],
  "verification": [],
  "needs_user_input": true,
  "confidence": 0.42
}`

	response, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(response.Actions) != 1 || response.Actions[0].FieldKey != "bot_token" {
		t.Fatalf("unexpected parsed actions %#v", response.Actions)
	}
}

func TestParseResponseRejectsMalformedAskUser(t *testing.T) {
	raw := `{
  "summary": "Need a token",
  "findings": [],
  "actions": [
    {
      "id": "ask-1",
      "type": "ask_user",
      "title": "Request token",
      "reason": "Setup is blocked without it",
      "input_kind": "secret",
      "prompt": "Paste the bot token"
    }
  ],
  "verification": [],
  "needs_user_input": true,
  "confidence": 0.42
}`

	_, err := ParseResponse(raw)
	if err == nil || !strings.Contains(err.Error(), "field_key") {
		t.Fatalf("expected missing field_key validation error, got %v", err)
	}
}

func TestParseResponseAcceptsNarration(t *testing.T) {
	raw := `{
  "summary": "Need to inspect tests.",
  "findings": [],
  "narration": {
    "turn_intent": "I found an existing test file, so I'll inspect it before editing.",
    "action_hints": [
      {
        "action_id": "read-tests",
        "text": "Let me read the current tests first."
      }
    ]
  },
  "actions": [
    {
      "id": "read-tests",
      "type": "read_files",
      "title": "Read tests",
      "reason": "Inspect current coverage",
      "path": "Modules/Tests"
    }
  ],
  "verification": [],
  "needs_user_input": false,
  "confidence": 0.8
}`

	response, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if response.Narration == nil || response.Narration.TurnIntent == "" {
		t.Fatalf("expected narration to parse, got %#v", response.Narration)
	}
	if len(response.Narration.ActionHints) != 1 || response.Narration.ActionHints[0].ActionID != "read-tests" {
		t.Fatalf("unexpected action hints %#v", response.Narration.ActionHints)
	}
}

func TestParseResponseDropsUnsafeNarration(t *testing.T) {
	raw := `{
  "summary": "Need to inspect tests.",
  "findings": [],
  "narration": {
    "turn_intent": "My chain-of-thought says to check the hidden token. Then inspect the file.",
    "action_hints": [
      {
        "action_id": "read-tests",
        "text": "I am using the system prompt and policy to decide this."
      }
    ]
  },
  "actions": [],
  "verification": [],
  "needs_user_input": false,
  "confidence": 0.8
}`

	response, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if response.Narration != nil {
		t.Fatalf("expected unsafe narration to be dropped, got %#v", response.Narration)
	}
}

func TestParseResponseKeepsEmptySummaryEmpty(t *testing.T) {
	raw := `{
  "summary": "",
  "findings": [],
  "actions": [],
  "verification": [],
  "needs_user_input": false,
  "confidence": 0.0
}`

	response, err := ParseResponse(raw)
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if response.Summary != "" {
		t.Fatalf("expected empty summary to remain empty, got %q", response.Summary)
	}
}
