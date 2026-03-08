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
