package llm

import (
	"context"
	"encoding/json"
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

func TestBuildAnthropicMessagesRequestIncludesEphemeralCacheControl(t *testing.T) {
	payload, err := buildAnthropicMessagesRequest(CompletionRequest{
		Model:    "claude-opus-4-6",
		Messages: []Message{{Role: RoleUser, Text: "hello"}},
	})
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
	token := requireLiveProviderToken(t, "ANTHROPIC_OAUTH_TOKEN")

	client := NewAnthropicClientWithOAuthToken(token)
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
