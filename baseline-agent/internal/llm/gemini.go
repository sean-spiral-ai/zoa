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

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type GeminiClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

func NewGeminiClient(apiKey string) *GeminiClient {
	return &GeminiClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 120 * time.Second},
		baseURL:    defaultBaseURL,
	}
}

type geminiGenerateRequest struct {
	SystemInstruction *geminiContent          `json:"system_instruction,omitempty"`
	Contents          []geminiContent         `json:"contents"`
	Tools             []geminiToolDefinitions `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generation_config,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature      float64        `json:"temperature,omitempty"`
	ResponseMimeType string         `json:"responseMimeType,omitempty"`
	ResponseSchema   map[string]any `json:"responseSchema,omitempty"`
}

type geminiToolDefinitions struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type geminiGenerateResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Error      *geminiAPIError   `json:"error,omitempty"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func (c *GeminiClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return CompletionResponse{}, errors.New("at least one message is required")
	}

	system, contents := toGeminiContents(req.Messages)
	if len(contents) == 0 {
		return CompletionResponse{}, errors.New("no user/assistant/tool messages to send")
	}

	payload := geminiGenerateRequest{
		Contents: contents,
	}
	if system != "" {
		payload.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: system}},
		}
	}
	if req.Temperature > 0 || strings.TrimSpace(req.ResponseMimeType) != "" || req.ResponseSchema != nil {
		payload.GenerationConfig = &geminiGenerationConfig{
			Temperature:      req.Temperature,
			ResponseMimeType: strings.TrimSpace(req.ResponseMimeType),
			ResponseSchema:   req.ResponseSchema,
		}
	}
	if len(req.Tools) > 0 {
		decls := make([]geminiFunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, geminiFunctionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			})
		}
		payload.Tools = []geminiToolDefinitions{{FunctionDeclarations: decls}}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	model := normalizeModel(req.Model)
	url := fmt.Sprintf("%s/%s:generateContent?key=%s", c.baseURL, model, c.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("gemini request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode >= 300 {
		var apiErr geminiGenerateResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error != nil {
			return CompletionResponse{}, fmt.Errorf("gemini error %d (%s): %s", apiErr.Error.Code, apiErr.Error.Status, apiErr.Error.Message)
		}
		return CompletionResponse{}, fmt.Errorf("gemini HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed geminiGenerateResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return CompletionResponse{}, fmt.Errorf("decode response: %w", err)
	}

	if parsed.Error != nil {
		return CompletionResponse{}, fmt.Errorf("gemini error %d (%s): %s", parsed.Error.Code, parsed.Error.Status, parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 {
		return CompletionResponse{}, errors.New("gemini returned no candidates")
	}

	candidate := parsed.Candidates[0]
	resp := CompletionResponse{}
	toolIdx := 0
	for _, p := range candidate.Content.Parts {
		if p.Text != "" {
			resp.Parts = append(resp.Parts, AssistantPart{
				Text:             p.Text,
				ThoughtSignature: p.ThoughtSignature,
			})
			if resp.Text == "" {
				resp.Text = p.Text
			} else {
				resp.Text += "\n" + p.Text
			}
		}
		if p.FunctionCall != nil {
			toolIdx++
			call := ToolCall{
				ID:               fmt.Sprintf("tool-%d", toolIdx),
				Name:             p.FunctionCall.Name,
				Args:             p.FunctionCall.Args,
				ThoughtSignature: p.ThoughtSignature,
			}
			resp.ToolCalls = append(resp.ToolCalls, call)
			resp.Parts = append(resp.Parts, AssistantPart{
				ToolCall:         &call,
				ThoughtSignature: p.ThoughtSignature,
			})
		}
	}

	return resp, nil
}

func normalizeModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if strings.HasPrefix(trimmed, "models/") {
		return trimmed
	}
	return "models/" + trimmed
}

func toGeminiContents(messages []Message) (string, []geminiContent) {
	system := ""
	contents := make([]geminiContent, 0, len(messages))

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
			contents = append(contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Text}},
			})
		case RoleAssistant:
			parts := make([]geminiPart, 0, len(m.Parts))
			for _, part := range m.Parts {
				if part.ToolCall != nil {
					call := part.ToolCall
					parts = append(parts, geminiPart{
						FunctionCall:     &geminiFunctionCall{Name: call.Name, Args: call.Args},
						ThoughtSignature: part.ThoughtSignature,
					})
					continue
				}
				if strings.TrimSpace(part.Text) == "" {
					continue
				}
				parts = append(parts, geminiPart{
					Text:             part.Text,
					ThoughtSignature: part.ThoughtSignature,
				})
			}
			// Backward-compatible fallback in case caller only populated Text/ToolCalls.
			if len(parts) == 0 {
				if strings.TrimSpace(m.Text) != "" {
					parts = append(parts, geminiPart{Text: m.Text})
				}
				for _, call := range m.ToolCalls {
					parts = append(parts, geminiPart{
						FunctionCall:     &geminiFunctionCall{Name: call.Name, Args: call.Args},
						ThoughtSignature: call.ThoughtSignature,
					})
				}
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, geminiContent{Role: "model", Parts: parts})
		case RoleTool:
			if len(m.ToolResults) == 0 {
				continue
			}
			parts := make([]geminiPart, 0, len(m.ToolResults))
			for _, result := range m.ToolResults {
				result := result
				parts = append(parts, geminiPart{FunctionResponse: &geminiFunctionResponse{
					Name: result.Name,
					Response: map[string]any{
						"call_id":  result.CallID,
						"output":   result.Output,
						"is_error": result.IsError,
					},
				}})
			}
			contents = append(contents, geminiContent{Role: "user", Parts: parts})
		}
	}

	return system, contents
}
