package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestToAnthropicMessagesToolUseIncludesEmptyInputObject(t *testing.T) {
	_, out := toAnthropicMessages([]Message{
		{
			Role: RoleAssistant,
			Parts: []AssistantPart{
				{
					ToolCall: &ToolCall{
						ID:   "call-1",
						Name: "demo_tool",
						Args: nil,
					},
				},
			},
		},
	})

	if len(out) != 1 {
		t.Fatalf("expected 1 outgoing message, got %d", len(out))
	}
	if len(out[0].Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(out[0].Content))
	}
	part := out[0].Content[0]
	if part.Type != "tool_use" {
		t.Fatalf("expected tool_use content type, got %q", part.Type)
	}
	if part.Input == nil {
		t.Fatalf("expected tool_use input object, got nil")
	}
	if len(part.Input) != 0 {
		t.Fatalf("expected empty input object, got %#v", part.Input)
	}
}

func TestBuildAnthropicMessagesRequestMarshalsEmptyToolInputObject(t *testing.T) {
	payload, err := buildAnthropicMessagesRequest(CompletionRequest{
		Model: "claude-opus-4-6",
		Messages: []Message{
			{
				Role: RoleAssistant,
				Parts: []AssistantPart{
					{
						ToolCall: &ToolCall{
							ID:   "call-1",
							Name: "demo_tool",
							Args: nil,
						},
					},
				},
			},
		},
	}, false)
	if err != nil {
		t.Fatalf("buildAnthropicMessagesRequest returned error: %v", err)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one message in JSON, got %#v", decoded["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first message object, got %#v", messages[0])
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one content part in JSON, got %#v", message["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected content object, got %#v", content[0])
	}
	input, ok := part["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected tool_use input object in JSON, got %#v", part["input"])
	}
	if len(input) != 0 {
		t.Fatalf("expected empty tool_use input object, got %#v", input)
	}
}

func TestBuildAnthropicMessagesRequestOmitsInputForTextContent(t *testing.T) {
	payload, err := buildAnthropicMessagesRequest(CompletionRequest{
		Model:    "claude-opus-4-6",
		Messages: []Message{{Role: RoleUser, Text: "hello"}},
	}, false)
	if err != nil {
		t.Fatalf("buildAnthropicMessagesRequest returned error: %v", err)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one message in JSON, got %#v", decoded["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first message object, got %#v", messages[0])
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one content part in JSON, got %#v", message["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected content object, got %#v", content[0])
	}
	if _, exists := part["input"]; exists {
		t.Fatalf("did not expect input field on text content, got %#v", part["input"])
	}
}

func TestBuildAnthropicMessagesRequestIncludesEphemeralCacheControl(t *testing.T) {
	payload, err := buildAnthropicMessagesRequest(CompletionRequest{
		Model:    "claude-opus-4-6",
		Messages: []Message{{Role: RoleUser, Text: "hello"}},
	}, false)
	if err != nil {
		t.Fatalf("buildAnthropicMessagesRequest returned error: %v", err)
	}
	if payload.CacheControl == nil {
		t.Fatal("expected cache_control to be set")
	}
	if payload.CacheControl.Type != "ephemeral" {
		t.Fatalf("expected cache_control type %q, got %q", "ephemeral", payload.CacheControl.Type)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	cacheControl, ok := decoded["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("expected cache_control object in JSON, got %#v", decoded["cache_control"])
	}
	if got := cacheControl["type"]; got != "ephemeral" {
		t.Fatalf("expected cache_control.type %q, got %#v", "ephemeral", got)
	}
}

func TestLiveSmokeAnthropic(t *testing.T) {
	token := requireLiveProviderToken(t, "ANTHROPIC_API_KEY")

	client := NewAnthropicClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	resp, err := client.Complete(ctx, CompletionRequest{
		Model:    "claude-opus-4-6",
		Messages: []Message{{Role: RoleUser, Text: "Reply with a short acknowledgement."}},
	})
	if err != nil {
		t.Fatalf("anthropic live smoke failed: %v", err)
	}
	if strings.TrimSpace(resp.Text) == "" && len(resp.ToolCalls) == 0 {
		t.Fatalf("anthropic returned empty response")
	}
}

func TestLiveAnthropicToolCallRoundTripPreservesInput(t *testing.T) {
	token := requireLiveProviderToken(t, "ANTHROPIC_API_KEY")

	client := NewAnthropicClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	resp, err := client.Complete(ctx, CompletionRequest{
		Model: "claude-opus-4-6",
		Messages: []Message{{
			Role: RoleUser,
			Text: "Call the tool `echo_required` exactly once with JSON input {\"value\":\"hello\"}. Do not say anything else.",
		}},
		Tools: []ToolSpec{{
			Name:        "echo_required",
			Description: "Test tool that requires a single string field named value.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{
						"type":        "string",
						"description": "Required string value.",
					},
				},
				"required": []string{"value"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("anthropic first round-trip call failed: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected exactly 1 tool call, got %d: %#v", len(resp.ToolCalls), resp.ToolCalls)
	}
	call := resp.ToolCalls[0]
	if call.Name != "echo_required" {
		t.Fatalf("expected tool name %q, got %q", "echo_required", call.Name)
	}
	value, ok := call.Args["value"].(string)
	if !ok {
		t.Fatalf("expected string tool arg value, got %#v", call.Args["value"])
	}
	if value != "hello" {
		t.Fatalf("expected tool arg value %q, got %q", "hello", value)
	}

	followupReq := CompletionRequest{
		Model: "claude-opus-4-6",
		Messages: []Message{
			{
				Role: RoleUser,
				Text: "Call the tool `echo_required` exactly once with JSON input {\"value\":\"hello\"}. Do not say anything else.",
			},
			{
				Role:      RoleAssistant,
				Parts:     []AssistantPart{{ToolCall: &call}},
				ToolCalls: []ToolCall{call},
			},
			{
				Role: RoleTool,
				ToolResults: []ToolResult{{
					CallID: call.ID,
					Name:   call.Name,
					Output: `{"ok":true}`,
				}},
			},
		},
		Tools: []ToolSpec{{
			Name:        "echo_required",
			Description: "Test tool that requires a single string field named value.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{
						"type":        "string",
						"description": "Required string value.",
					},
				},
				"required": []string{"value"},
			},
		}},
	}

	payload, err := buildAnthropicMessagesRequest(followupReq, false)
	if err != nil {
		t.Fatalf("buildAnthropicMessagesRequest follow-up returned error: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal follow-up returned error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal follow-up returned error: %v", err)
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("expected 3 follow-up messages, got %#v", decoded["messages"])
	}
	assistantMessage, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("expected assistant message object, got %#v", messages[1])
	}
	content, ok := assistantMessage["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected 1 assistant content part, got %#v", assistantMessage["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected assistant content object, got %#v", content[0])
	}
	input, ok := part["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected follow-up tool_use input object, got %#v", part["input"])
	}
	if got, ok := input["value"].(string); !ok || got != "hello" {
		t.Fatalf("expected follow-up tool_use input value %q, got %#v", "hello", input["value"])
	}
}

func TestAnthropicCompleteIncludesStatusAndBodyForNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{
			"type":"error",
			"error":{
				"type":"invalid_request_error",
				"message":"Error"
			},
			"debug":"tools.0.input_schema.properties.path.type must be specified"
		}`))
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-token")
	client.url = srv.URL

	_, err := client.Complete(context.Background(), CompletionRequest{
		Model:    "claude-opus-4-6",
		Messages: []Message{{Role: RoleUser, Text: "What is in the SwimLife directory?"}},
	})
	if err == nil {
		t.Fatal("expected non-2xx error")
	}

	errText := err.Error()
	if !strings.Contains(errText, "anthropic HTTP 400 error (invalid_request_error): Error") {
		t.Fatalf("expected HTTP status in error, got %q", errText)
	}
	if !strings.Contains(errText, `[body="{`) {
		t.Fatalf("expected body excerpt in error, got %q", errText)
	}
	if !strings.Contains(errText, `tools.0.input_schema.properties.path.type must be specified`) {
		t.Fatalf("expected sanitized body excerpt in error, got %q", errText)
	}
}

func TestBuildAnthropicMessagesRequestPrependsClaudeCodeSystemForSetupTokens(t *testing.T) {
	payload, err := buildAnthropicMessagesRequest(CompletionRequest{
		Model: "claude-opus-4-6",
		Messages: []Message{
			{Role: RoleSystem, Text: "Keep responses short."},
			{Role: RoleUser, Text: "hello"},
		},
	}, true)
	if err != nil {
		t.Fatalf("buildAnthropicMessagesRequest returned error: %v", err)
	}
	if len(payload.System) != 2 {
		t.Fatalf("expected 2 system parts, got %#v", payload.System)
	}
	if payload.System[0].Text != anthropicClaudeCodeSystemPrompt {
		t.Fatalf("expected Claude Code system prompt first, got %#v", payload.System[0])
	}
	if payload.System[1].Text != "Keep responses short." {
		t.Fatalf("expected caller system prompt second, got %#v", payload.System[1])
	}
}

func TestBuildAnthropicMessagesRequestOmitsClaudeCodeSystemForAPIKeys(t *testing.T) {
	payload, err := buildAnthropicMessagesRequest(CompletionRequest{
		Model: "claude-opus-4-6",
		Messages: []Message{
			{Role: RoleSystem, Text: "Keep responses short."},
			{Role: RoleUser, Text: "hello"},
		},
	}, false)
	if err != nil {
		t.Fatalf("buildAnthropicMessagesRequest returned error: %v", err)
	}
	if len(payload.System) != 1 {
		t.Fatalf("expected 1 system part, got %#v", payload.System)
	}
	if payload.System[0].Text != "Keep responses short." {
		t.Fatalf("expected caller system prompt only, got %#v", payload.System[0])
	}
}
