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

type OpenAICompatibleClient struct {
	baseURL string
	apiKey  string
	headers map[string]string
	client  *http.Client
}

type chatCompletionRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature,omitempty"`
	ResponseFormat responseFormat `json:"response_format"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func NewOpenAICompatibleClient(baseURL, apiKey string, headers map[string]string) *OpenAICompatibleClient {
	return &OpenAICompatibleClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		headers: headers,
		client: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

func (c *OpenAICompatibleClient) Generate(ctx context.Context, req Request) (response core.LLMResponse, err error) {
	endpoint := c.baseURL + "/chat/completions"

	messages := make([]chatMessage, 0, len(req.Messages)+1)
	messages = append(messages, chatMessage{
		Role:    "system",
		Content: req.System,
	})
	for _, msg := range req.Messages {
		messages = append(messages, chatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	temp := req.Temperature
	if strings.HasPrefix(req.Model, "gpt-5") || strings.HasPrefix(req.Model, "o1") || strings.HasPrefix(req.Model, "o3") {
		temp = 1
	}

	payload := chatCompletionRequest{
		Model:       req.Model,
		Messages:    messages,
		Temperature: temp,
		ResponseFormat: responseFormat{
			Type: "json_object",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return core.LLMResponse{}, fmt.Errorf("marshal provider request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return core.LLMResponse{}, fmt.Errorf("build provider request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	for key, value := range c.headers {
		httpReq.Header.Set(key, value)
	}

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return core.LLMResponse{}, fmt.Errorf("provider request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, 2*1024*1024))
	if err != nil {
		return core.LLMResponse{}, fmt.Errorf("read provider response: %w", err)
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(respBytes, &decoded); err != nil {
		return core.LLMResponse{}, fmt.Errorf("decode provider envelope: %w", err)
	}
	if decoded.Error != nil {
		return core.LLMResponse{}, fmt.Errorf("provider error: %s", decoded.Error.Message)
	}
	if httpResp.StatusCode >= 400 {
		return core.LLMResponse{}, fmt.Errorf("provider request failed with %s", httpResp.Status)
	}
	if len(decoded.Choices) == 0 {
		return core.LLMResponse{}, fmt.Errorf("provider returned no choices")
	}

	response, err = ParseResponse(decoded.Choices[0].Message.Content)
	if err != nil {
		return core.LLMResponse{}, err
	}
	response.Usage = core.TokenUsage{
		PromptTokens:     decoded.Usage.PromptTokens,
		CompletionTokens: decoded.Usage.CompletionTokens,
		TotalTokens:      decoded.Usage.TotalTokens,
	}
	return response, nil
}

func (c *OpenAICompatibleClient) WebSearch(ctx context.Context, req SearchRequest) ([]string, error) {
	return nil, ErrWebSearchUnsupported
}
