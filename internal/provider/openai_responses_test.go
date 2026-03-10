package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/richardsondx/IronLark/internal/core"
)

func TestOpenAIResponsesClientGenerateParsesStructuredResponse(t *testing.T) {
	var captured responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output_text": `{"summary":"ok","findings":[],"actions":[],"verification":[],"needs_user_input":false,"confidence":0.91}`,
			"usage": map[string]any{
				"input_tokens":  11,
				"output_tokens": 7,
				"total_tokens":  18,
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient(server.URL, "test-key", nil)
	response, err := client.Generate(context.Background(), Request{
		Model:       "gpt-5-mini",
		System:      "system prompt",
		Messages:    []core.ConversationMessage{{Role: "user", Content: "hello"}},
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Summary != "ok" {
		t.Fatalf("unexpected response %#v", response)
	}
	if response.Usage.TotalTokens != 18 || response.Usage.PromptTokens != 11 || response.Usage.CompletionTokens != 7 {
		t.Fatalf("unexpected usage %#v", response.Usage)
	}
	if captured.Instructions != "system prompt" {
		t.Fatalf("expected instructions to carry system prompt, got %#v", captured)
	}
	if captured.Text == nil || captured.Text.Format.Type != "json_schema" {
		t.Fatalf("expected json schema response format, got %#v", captured.Text)
	}
	if len(captured.Input) != 1 || len(captured.Input[0].Content) != 1 || captured.Input[0].Content[0].Type != "input_text" {
		t.Fatalf("expected user message to be encoded as input_text, got %#v", captured.Input)
	}
}

func TestOpenAIResponsesClientWebSearchParsesResults(t *testing.T) {
	var captured responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{
				{
					"type": "message",
					"content": []map[string]any{
						{
							"type": "output_text",
							"text": `{"results":["Harbor docs | https://harborframework.com/docs/agents | agent setup","Installed agents | https://harborframework.com/docs/installed-agents | benchmark notes"]}`,
						},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  20,
				"output_tokens": 14,
				"total_tokens":  34,
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIResponsesClient(server.URL, "test-key", nil)
	results, err := client.WebSearch(context.Background(), SearchRequest{
		Model:      "gpt-5-mini",
		Query:      "site:harborframework.com/docs Agents BaseEnvironment Installed agents",
		MaxResults: 1,
	})
	if err != nil {
		t.Fatalf("WebSearch() error = %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0], "Harbor docs") {
		t.Fatalf("unexpected results %#v", results)
	}
	if len(captured.Tools) != 1 {
		t.Fatalf("expected one web search tool, got %#v", captured.Tools)
	}
	if captured.Tools[0].Type != "web_search" && captured.Tools[0].Type != "web_search_preview" {
		t.Fatalf("unexpected tool type %#v", captured.Tools)
	}
}

func TestResponsesInputFromMessagesEncodesAssistantHistoryAsOutputText(t *testing.T) {
	items := responsesInputFromMessages([]core.ConversationMessage{
		{Role: "user", Content: "find docs"},
		{Role: "assistant", Content: `{"summary":"ok"}`},
	})
	if len(items) != 2 {
		t.Fatalf("unexpected items %#v", items)
	}
	if items[0].Content[0].Type != "input_text" {
		t.Fatalf("expected user content to be input_text, got %#v", items[0])
	}
	if items[1].Content[0].Type != "output_text" {
		t.Fatalf("expected assistant content to be output_text, got %#v", items[1])
	}
}
