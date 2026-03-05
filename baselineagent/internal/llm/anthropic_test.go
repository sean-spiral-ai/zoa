package llm

import (
	"context"
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
