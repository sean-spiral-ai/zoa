package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const anthropicMessagesURL = "https://api.anthropic.com/v1/messages"
const anthropicVersionHeader = "2023-06-01"
const anthropicOAuthBetaHeader = "oauth-2025-04-20"
const defaultAnthropicMaxTokens = 4096

type AnthropicClient struct {
	oauthToken string
	httpClient *http.Client
	url        string
}

func NewAnthropicClientWithOAuthToken(token string) *AnthropicClient {
	return &AnthropicClient{
		oauthToken: strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: 120 * time.Second},
		url:        anthropicMessagesURL,
	}
}

type anthropicMessagesRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicToolSpec    `json:"tools,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
}

type anthropicOutputConfig struct {
	Format anthropicOutputFormat `json:"format"`
}

type anthropicOutputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema,omitempty"`
}

type anthropicToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type anthropicMessage struct {
	Role    string                 `json:"role"`
	Content []anthropicContentPart `json:"content"`
}

type anthropicContentPart struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type anthropicMessagesResponse struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Role       string                 `json:"role"`
	Model      string                 `json:"model"`
	Content    []anthropicContentPart `json:"content"`
	StopReason string                 `json:"stop_reason"`
	Error      *anthropicAPIError     `json:"error,omitempty"`
}

type anthropicErrorEnvelope struct {
	Type  string             `json:"type"`
	Error *anthropicAPIError `json:"error,omitempty"`
}

type anthropicAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (c *AnthropicClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if strings.TrimSpace(c.oauthToken) == "" {
		return CompletionResponse{}, errors.New("oauth token is required")
	}
	if len(req.Messages) == 0 {
		return CompletionResponse{}, errors.New("at least one message is required")
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		return CompletionResponse{}, errors.New("model is required")
	}

	payload, err := buildAnthropicMessagesRequest(req)
	if err != nil {
		return CompletionResponse{}, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersionHeader)
	httpReq.Header.Set("anthropic-beta", anthropicOAuthBetaHeader)
	httpReq.Header.Set("Authorization", "Bearer "+c.oauthToken)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode >= 300 {
		var apiErr anthropicErrorEnvelope
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error != nil {
			return CompletionResponse{}, fmt.Errorf("anthropic error (%s): %s", apiErr.Error.Type, apiErr.Error.Message)
		}
		return CompletionResponse{}, fmt.Errorf("anthropic HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed anthropicMessagesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return CompletionResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Error != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic error (%s): %s", parsed.Error.Type, parsed.Error.Message)
	}
	if len(parsed.Content) == 0 {
		return CompletionResponse{}, errors.New("anthropic returned no content")
	}

	resp := CompletionResponse{}
	toolIdx := 0
	for _, part := range parsed.Content {
		switch part.Type {
		case "text":
			if part.Text == "" {
				continue
			}
			resp.Parts = append(resp.Parts, AssistantPart{Text: part.Text})
			if resp.Text == "" {
				resp.Text = part.Text
			} else {
				resp.Text += "\n" + part.Text
			}
		case "tool_use":
			toolIdx++
			callID := strings.TrimSpace(part.ID)
			if callID == "" {
				callID = fmt.Sprintf("tool-%d", toolIdx)
			}
			call := ToolCall{
				ID:   callID,
				Name: part.Name,
				Args: part.Input,
			}
			resp.ToolCalls = append(resp.ToolCalls, call)
			resp.Parts = append(resp.Parts, AssistantPart{ToolCall: &call})
		}
	}

	return resp, nil
}

func buildAnthropicMessagesRequest(req CompletionRequest) (anthropicMessagesRequest, error) {
	system, messages := toAnthropicMessages(req.Messages)
	if len(messages) == 0 {
		return anthropicMessagesRequest{}, errors.New("no user/assistant/tool messages to send")
	}

	payload := anthropicMessagesRequest{
		Model:     strings.TrimSpace(req.Model),
		MaxTokens: defaultAnthropicMaxTokens,
		System:    system,
		Messages:  messages,
	}
	if req.Temperature > 0 {
		temp := req.Temperature
		payload.Temperature = &temp
	}
	if len(req.Tools) > 0 {
		payload.Tools = make([]anthropicToolSpec, 0, len(req.Tools))
		for _, tool := range req.Tools {
			payload.Tools = append(payload.Tools, anthropicToolSpec{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.Schema,
			})
		}
	}

	if strings.TrimSpace(req.ResponseMimeType) == "application/json" {
		if req.ResponseSchema != nil {
			payload.OutputConfig = &anthropicOutputConfig{
				Format: anthropicOutputFormat{
					Type:   "json_schema",
					Schema: req.ResponseSchema,
				},
			}
		}
	}

	return payload, nil
}

func toAnthropicMessages(messages []Message) (string, []anthropicMessage) {
	system := ""
	out := make([]anthropicMessage, 0, len(messages))

	for _, m := range messages {
		switch m.Role {
		case RoleSystem:
			if strings.TrimSpace(m.Text) == "" {
				continue
			}
			if system == "" {
				system = m.Text
			} else {
				system += "\n\n" + m.Text
			}
		case RoleUser:
			if strings.TrimSpace(m.Text) == "" {
				continue
			}
			out = append(out, anthropicMessage{
				Role: "user",
				Content: []anthropicContentPart{{
					Type: "text",
					Text: m.Text,
				}},
			})
		case RoleAssistant:
			parts := make([]anthropicContentPart, 0, len(m.Parts))
			for _, part := range m.Parts {
				if part.ToolCall != nil {
					call := part.ToolCall
					callID := strings.TrimSpace(call.ID)
					if callID == "" {
						callID = fmt.Sprintf("tool-%d", len(parts)+1)
					}
					parts = append(parts, anthropicContentPart{
						Type:  "tool_use",
						ID:    callID,
						Name:  call.Name,
						Input: call.Args,
					})
					continue
				}
				if strings.TrimSpace(part.Text) == "" {
					continue
				}
				parts = append(parts, anthropicContentPart{
					Type: "text",
					Text: part.Text,
				})
			}

			// Backward-compatible fallback in case caller only populated Text/ToolCalls.
			if len(parts) == 0 {
				if strings.TrimSpace(m.Text) != "" {
					parts = append(parts, anthropicContentPart{
						Type: "text",
						Text: m.Text,
					})
				}
				for idx, call := range m.ToolCalls {
					callID := strings.TrimSpace(call.ID)
					if callID == "" {
						callID = fmt.Sprintf("tool-%d", idx+1)
					}
					parts = append(parts, anthropicContentPart{
						Type:  "tool_use",
						ID:    callID,
						Name:  call.Name,
						Input: call.Args,
					})
				}
			}

			if len(parts) == 0 {
				continue
			}
			out = append(out, anthropicMessage{
				Role:    "assistant",
				Content: parts,
			})
		case RoleTool:
			if len(m.ToolResults) == 0 {
				continue
			}
			parts := make([]anthropicContentPart, 0, len(m.ToolResults))
			for idx, result := range m.ToolResults {
				callID := strings.TrimSpace(result.CallID)
				if callID == "" {
					callID = fmt.Sprintf("tool-result-%d", idx+1)
				}
				parts = append(parts, anthropicContentPart{
					Type:      "tool_result",
					ToolUseID: callID,
					Content:   result.Output,
					IsError:   result.IsError,
				})
			}
			out = append(out, anthropicMessage{
				Role:    "user",
				Content: parts,
			})
		}
	}

	return system, out
}
