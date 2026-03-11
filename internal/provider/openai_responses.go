package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

type OpenAIResponsesClient struct {
	baseURL string
	apiKey  string
	headers map[string]string
	client  *http.Client
}

type responsesRequest struct {
	Model        string               `json:"model"`
	Instructions string               `json:"instructions,omitempty"`
	Input        []responsesInputItem `json:"input,omitempty"`
	Tools        []responsesTool      `json:"tools,omitempty"`
	Text         *responsesTextConfig `json:"text,omitempty"`
	Temperature  float64              `json:"temperature,omitempty"`
	ToolChoice   any                  `json:"tool_choice,omitempty"`
	Include      []string             `json:"include,omitempty"`
}

type responsesInputItem struct {
	Role    string                  `json:"role"`
	Content []responsesContentInput `json:"content"`
}

type responsesContentInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesTool struct {
	Type string `json:"type"`
}

type responsesTextConfig struct {
	Format responsesTextFormat `json:"format"`
}

type responsesTextFormat struct {
	Type        string         `json:"type"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Strict      bool           `json:"strict,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
}

type responsesAPIResponse struct {
	Output     []responsesOutputItem `json:"output"`
	OutputText string                `json:"output_text"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type responsesOutputItem struct {
	Type    string                   `json:"type"`
	Status  string                   `json:"status,omitempty"`
	Role    string                   `json:"role,omitempty"`
	Content []responsesOutputContent `json:"content,omitempty"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func NewOpenAIResponsesClient(baseURL, apiKey string, headers map[string]string) *OpenAIResponsesClient {
	return &OpenAIResponsesClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		headers: headers,
		client: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

func (c *OpenAIResponsesClient) Generate(ctx context.Context, req Request) (core.LLMResponse, error) {
	payload := responsesRequest{
		Model:        req.Model,
		Instructions: req.System,
		Input:        responsesInputFromMessages(req.Messages),
		Text: &responsesTextConfig{
			Format: responsesTextFormat{
				Type:        "json_schema",
				Name:        "ironlark_response",
				Description: "IronLark action planning response",
				Strict:      true,
				Schema:      llmResponseSchema(),
			},
		},
	}
	if temp := effectiveTemperature(req.Model, req.Temperature); temp != 0 {
		payload.Temperature = temp
	}
	if len(req.Tools) > 0 {
		payload.Tools = responsesToolsFromSpecs(req.Tools)
	}

	decoded, err := c.createResponse(ctx, payload)
	if err != nil {
		return core.LLMResponse{}, err
	}
	raw := extractResponseText(decoded)
	if strings.TrimSpace(raw) == "" {
		return core.LLMResponse{}, fmt.Errorf("provider returned no output text")
	}
	response, err := ParseResponse(raw)
	if err != nil {
		return core.LLMResponse{}, err
	}
	response.Usage = core.TokenUsage{
		PromptTokens:     decoded.Usage.InputTokens,
		CompletionTokens: decoded.Usage.OutputTokens,
		TotalTokens:      decoded.Usage.TotalTokens,
	}
	return response, nil
}

func (c *OpenAIResponsesClient) WebSearch(ctx context.Context, req SearchRequest) ([]string, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("web search query cannot be empty")
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}

	prompt := fmt.Sprintf("Search the web for: %s\nReturn up to %d concise results as JSON.", req.Query, req.MaxResults)
	payload := responsesRequest{
		Model: req.Model,
		Input: []responsesInputItem{
			{
				Role: "user",
				Content: []responsesContentInput{
					{Type: "input_text", Text: prompt},
				},
			},
		},
		Text: &responsesTextConfig{
			Format: responsesTextFormat{
				Type:        "json_schema",
				Name:        "web_search_results",
				Description: "Structured web search results for IronLark",
				Strict:      true,
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"results": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "string",
							},
						},
					},
					"required":             []string{"results"},
					"additionalProperties": false,
				},
			},
		},
	}

	toolVariants := []string{"web_search", "web_search_preview"}
	var lastErr error
	for _, toolType := range toolVariants {
		payload.Tools = []responsesTool{{Type: toolType}}
		decoded, err := c.createResponse(ctx, payload)
		if err != nil {
			lastErr = err
			if !strings.Contains(strings.ToLower(err.Error()), "web_search") {
				return nil, err
			}
			continue
		}
		raw := extractResponseText(decoded)
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("provider returned no output text for web search")
		}
		var parsed struct {
			Results []string `json:"results"`
		}
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return nil, fmt.Errorf("decode provider web search response: %w", err)
		}
		if len(parsed.Results) > req.MaxResults {
			parsed.Results = parsed.Results[:req.MaxResults]
		}
		return parsed.Results, nil
	}
	if lastErr == nil {
		lastErr = ErrWebSearchUnsupported
	}
	return nil, lastErr
}

func (c *OpenAIResponsesClient) createResponse(ctx context.Context, payload responsesRequest) (responsesAPIResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return responsesAPIResponse{}, fmt.Errorf("marshal provider request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return responsesAPIResponse{}, fmt.Errorf("build provider request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	for key, value := range c.headers {
		httpReq.Header.Set(key, value)
	}

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return responsesAPIResponse{}, fmt.Errorf("provider request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, 2*1024*1024))
	if err != nil {
		return responsesAPIResponse{}, fmt.Errorf("read provider response: %w", err)
	}

	var decoded responsesAPIResponse
	if err := json.Unmarshal(respBytes, &decoded); err != nil {
		return responsesAPIResponse{}, fmt.Errorf("decode provider envelope: %w", err)
	}
	if decoded.Error != nil {
		return responsesAPIResponse{}, fmt.Errorf("provider error: %s", decoded.Error.Message)
	}
	if httpResp.StatusCode >= 400 {
		return responsesAPIResponse{}, fmt.Errorf("provider request failed with %s", httpResp.Status)
	}
	return decoded, nil
}

func responsesInputFromMessages(messages []core.ConversationMessage) []responsesInputItem {
	out := make([]responsesInputItem, 0, len(messages))
	for _, msg := range messages {
		contentType := "input_text"
		if strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			contentType = "output_text"
		}
		out = append(out, responsesInputItem{
			Role: msg.Role,
			Content: []responsesContentInput{
				{Type: contentType, Text: msg.Content},
			},
		})
	}
	return out
}

func responsesToolsFromSpecs(specs []ToolSpec) []responsesTool {
	out := make([]responsesTool, 0, len(specs))
	for _, spec := range specs {
		switch spec.Type {
		case ToolWebSearch:
			out = append(out, responsesTool{Type: "web_search"})
		}
	}
	return out
}

func effectiveTemperature(model string, value float64) float64 {
	if strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") {
		return 1
	}
	return value
}

func extractResponseText(resp responsesAPIResponse) string {
	if strings.TrimSpace(resp.OutputText) != "" {
		return resp.OutputText
	}
	parts := []string{}
	for _, item := range resp.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" || content.Type == "text" {
				text := strings.TrimSpace(content.Text)
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func llmResponseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"findings": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"narration": map[string]any{
				"type": []string{"object", "null"},
				"properties": map[string]any{
					"turn_intent": map[string]any{"type": "string"},
					"action_hints": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"action_id": map[string]any{"type": "string"},
								"text":      map[string]any{"type": "string"},
							},
							"required":             []string{"action_id", "text"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"turn_intent", "action_hints"},
				"additionalProperties": false,
			},
			"actions": map[string]any{
				"type":  "array",
				"items": actionSchema(),
			},
			"verification": map[string]any{
				"type":  "array",
				"items": verificationSchema(),
			},
			"needs_user_input": map[string]any{"type": "boolean"},
			"confidence":       map[string]any{"type": "number"},
		},
		"required":             []string{"summary", "findings", "narration", "actions", "verification", "needs_user_input", "confidence"},
		"additionalProperties": false,
	}
}

func actionSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":                 map[string]any{"type": "string"},
			"type":               map[string]any{"type": "string"},
			"title":              map[string]any{"type": "string"},
			"reason":             map[string]any{"type": "string"},
			"command":            nullableStringSchema(),
			"path":               nullableStringSchema(),
			"paths":              nullableStringArraySchema(),
			"query":              nullableStringSchema(),
			"pattern":            nullableStringSchema(),
			"glob":               nullableStringSchema(),
			"patch_unified_diff": nullableStringSchema(),
			"content":            nullableStringSchema(),
			"file_mode":          nullableStringSchema(),
			"timeout_sec":        nullableIntegerSchema(),
			"detach":             nullableBoolSchema(),
			"input_kind":         nullableStringSchema(),
			"field_key":          nullableStringSchema(),
			"prompt":             nullableStringSchema(),
			"clarification":      nullableStringSchema(),
			"placeholder":        nullableStringSchema(),
			"destination_hint":   nullableStringSchema(),
			"expects_value":      nullableBoolSchema(),
			"alternatives":       nullableStringArraySchema(),
		},
		"required":             []string{"id", "type", "title", "reason", "command", "path", "paths", "query", "pattern", "glob", "patch_unified_diff", "content", "file_mode", "timeout_sec", "detach", "input_kind", "field_key", "prompt", "clarification", "placeholder", "destination_hint", "expects_value", "alternatives"},
		"additionalProperties": false,
	}
}

func verificationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":         map[string]any{"type": "string"},
			"command":      nullableStringSchema(),
			"path":         nullableStringSchema(),
			"paths":        nullableStringArraySchema(),
			"success_hint": nullableStringSchema(),
			"timeout_sec":  nullableIntegerSchema(),
		},
		"required":             []string{"type", "command", "path", "paths", "success_hint", "timeout_sec"},
		"additionalProperties": false,
	}
}

func nullableStringSchema() map[string]any {
	return map[string]any{"type": []string{"string", "null"}}
}

func nullableStringArraySchema() map[string]any {
	return map[string]any{
		"type": []string{"array", "null"},
		"items": map[string]any{
			"type": "string",
		},
	}
}

func nullableBoolSchema() map[string]any {
	return map[string]any{"type": []string{"boolean", "null"}}
}

func nullableIntegerSchema() map[string]any {
	return map[string]any{"type": []string{"integer", "null"}}
}
