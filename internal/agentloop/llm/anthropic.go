package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"zoa/internal/semtrace"
)

const anthropicMessagesURL = "https://api.anthropic.com/v1/messages"
const anthropicVersionHeader = "2023-06-01"
const defaultAnthropicMaxTokens = 64000
const anthropicSetupTokenPrefix = "sk-ant-oat"
const anthropicClaudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

type AnthropicClient struct {
	apiKey     string
	httpClient *http.Client
	url        string
}

func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: &http.Client{},
		url:        anthropicMessagesURL,
	}
}

type anthropicMessagesRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	System       []anthropicSystemPart  `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicToolSpec    `json:"tools,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
	Temperature  *float64               `json:"temperature,omitempty"`
}

type anthropicSystemPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
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
	Input     map[string]any `json:"-"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

func (p anthropicContentPart) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"type": p.Type,
	}
	if p.Text != "" {
		out["text"] = p.Text
	}
	if p.ID != "" {
		out["id"] = p.ID
	}
	if p.Name != "" {
		out["name"] = p.Name
	}
	if p.Type == "tool_use" {
		out["input"] = normalizeToolCallArgs(p.Input)
	}
	if p.ToolUseID != "" {
		out["tool_use_id"] = p.ToolUseID
	}
	if p.Content != "" {
		out["content"] = p.Content
	}
	if p.IsError {
		out["is_error"] = p.IsError
	}
	return json.Marshal(out)
}

func (p *anthropicContentPart) UnmarshalJSON(data []byte) error {
	type anthropicContentPartJSON struct {
		Type      string         `json:"type"`
		Text      string         `json:"text,omitempty"`
		ID        string         `json:"id,omitempty"`
		Name      string         `json:"name,omitempty"`
		Input     map[string]any `json:"input,omitempty"`
		ToolUseID string         `json:"tool_use_id,omitempty"`
		Content   string         `json:"content,omitempty"`
		IsError   bool           `json:"is_error,omitempty"`
	}
	var decoded anthropicContentPartJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	p.Type = decoded.Type
	p.Text = decoded.Text
	p.ID = decoded.ID
	p.Name = decoded.Name
	p.Input = decoded.Input
	p.ToolUseID = decoded.ToolUseID
	p.Content = decoded.Content
	p.IsError = decoded.IsError
	return nil
}

type anthropicMessagesResponse struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Role       string                 `json:"role"`
	Model      string                 `json:"model"`
	Content    []anthropicContentPart `json:"content"`
	Usage      *anthropicUsage        `json:"usage,omitempty"`
	StopReason string                 `json:"stop_reason"`
	Error      *anthropicAPIError     `json:"error,omitempty"`
}

type anthropicUsage struct {
	InputTokens             *int `json:"input_tokens,omitempty"`
	OutputTokens            *int `json:"output_tokens,omitempty"`
	CacheCreationInputToken *int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens    *int `json:"cache_read_input_tokens,omitempty"`
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
	if ctx == nil {
		ctx = context.Background()
	}
	model := strings.TrimSpace(req.Model)
	ctx, traceRegion := semtrace.StartRegionWithAttrs(ctx, "llm.anthropic.complete", map[string]any{
		"provider": "anthropic",
		"model":    model,
	})
	endAttrs := map[string]any{}
	defer func() { traceRegion.EndWithAttrs(endAttrs) }()
	semtrace.LogAttrs(ctx, "llm", "anthropic request", map[string]any{
		"provider":          "anthropic",
		"model":             model,
		"messages":          len(req.Messages),
		"tools":             len(req.Tools),
		"max_output_tokens": req.MaxOutputTokens,
	})

	if strings.TrimSpace(c.apiKey) == "" {
		return CompletionResponse{}, errors.New("api key is required")
	}
	if len(req.Messages) == 0 {
		return CompletionResponse{}, errors.New("at least one message is required")
	}

	if model == "" {
		return CompletionResponse{}, errors.New("model is required")
	}

	payload, err := buildAnthropicMessagesRequest(req, isAnthropicSetupToken(c.apiKey))
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
	httpReq.Header.Set("x-api-key", c.apiKey)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		semtrace.LogAttrs(ctx, "llm.error", err.Error(), map[string]any{
			"provider": "anthropic",
			"model":    model,
		})
		return CompletionResponse{}, fmt.Errorf("anthropic request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode >= 300 {
		bodySummary := summarizeHTTPErrorBody(respBody)
		var apiErr anthropicErrorEnvelope
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error != nil {
			semtrace.LogAttrs(ctx, "llm.error", "anthropic non-2xx", map[string]any{
				"provider":     "anthropic",
				"model":        model,
				"status":       httpResp.StatusCode,
				"body_excerpt": bodySummary,
			})
			return CompletionResponse{}, fmt.Errorf(
				"anthropic HTTP %d error (%s): %s [body=%s]",
				httpResp.StatusCode,
				apiErr.Error.Type,
				apiErr.Error.Message,
				bodySummary,
			)
		}
		semtrace.LogAttrs(ctx, "llm.error", "anthropic non-2xx", map[string]any{
			"provider":     "anthropic",
			"model":        model,
			"status":       httpResp.StatusCode,
			"body_excerpt": bodySummary,
		})
		return CompletionResponse{}, fmt.Errorf("anthropic HTTP %d: %s", httpResp.StatusCode, bodySummary)
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
	semtrace.LogAttrs(ctx, "llm", "anthropic response", map[string]any{
		"provider":   "anthropic",
		"model":      model,
		"text_len":   len(resp.Text),
		"tool_calls": len(resp.ToolCalls),
	})
	if parsed.Usage != nil {
		if parsed.Usage.InputTokens != nil {
			endAttrs["tokens.input"] = *parsed.Usage.InputTokens
		}
		if parsed.Usage.OutputTokens != nil {
			endAttrs["tokens.output"] = *parsed.Usage.OutputTokens
		}
		if parsed.Usage.CacheCreationInputToken != nil {
			endAttrs["tokens.cache_creation_input"] = *parsed.Usage.CacheCreationInputToken
		}
		if parsed.Usage.CacheReadInputTokens != nil {
			endAttrs["tokens.cache_read_input"] = *parsed.Usage.CacheReadInputTokens
		}
	}

	return resp, nil
}

func buildAnthropicMessagesRequest(req CompletionRequest, setupToken bool) (anthropicMessagesRequest, error) {
	systemText, messages := toAnthropicMessages(req.Messages)
	if len(messages) == 0 {
		return anthropicMessagesRequest{}, errors.New("no user/assistant/tool messages to send")
	}

	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultAnthropicMaxTokens
	}
	payload := anthropicMessagesRequest{
		Model:        strings.TrimSpace(req.Model),
		MaxTokens:    maxTokens,
		System:       buildAnthropicSystemParts(systemText, setupToken),
		Messages:     messages,
		CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"},
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

func buildAnthropicSystemParts(systemText string, setupToken bool) []anthropicSystemPart {
	parts := make([]anthropicSystemPart, 0, 2)
	if setupToken {
		// Anthropic setup-tokens can reject Opus requests unless the payload carries
		// the Claude Code identity block that OpenClaw and Claude Code include.
		parts = append(parts, anthropicSystemPart{
			Type: "text",
			Text: anthropicClaudeCodeSystemPrompt,
		})
	}
	if strings.TrimSpace(systemText) != "" {
		parts = append(parts, anthropicSystemPart{
			Type: "text",
			Text: systemText,
		})
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
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
						Input: normalizeToolCallArgs(call.Args),
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
						Input: normalizeToolCallArgs(call.Args),
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

func normalizeToolCallArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	return args
}

func summarizeHTTPErrorBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return `""`
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxLen = 400
	if len(text) > maxLen {
		text = text[:maxLen] + "..."
	}
	return strconv.Quote(text)
}

func isAnthropicSetupToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), anthropicSetupTokenPrefix)
}
