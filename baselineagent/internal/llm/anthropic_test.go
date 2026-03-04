package llm

import "testing"

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
