package agent

import (
	"context"
	"testing"

	builtintools "zoa/baselineagent/builtintools"
	"zoa/baselineagent/internal/llm"
)

type testLLMClient struct {
	complete func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error)
}

func (c *testLLMClient) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return c.complete(ctx, req)
}

type echoTool struct{}

func (echoTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "echo",
		Description: "echo tool",
		Schema:      map[string]any{"type": "object"},
	}
}

func (echoTool) Execute(_ context.Context, args map[string]any) (string, error) {
	if args == nil {
		return "{}", nil
	}
	return "ok", nil
}

func TestPromptWithOptions_OnMessageReceivesUserAssistantToolIncrementally(t *testing.T) {
	client := &testLLMClient{
		complete: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			switch len(req.Messages) {
			case 2: // system + first user message
				return llm.CompletionResponse{
					ToolCalls: []llm.ToolCall{{
						ID:   "call-1",
						Name: "echo",
						Args: map[string]any{"x": 1},
					}},
					Parts: []llm.AssistantPart{{
						ToolCall: &llm.ToolCall{
							ID:   "call-1",
							Name: "echo",
							Args: map[string]any{"x": 1},
						},
					}},
				}, nil
			default:
				return llm.CompletionResponse{
					Text: "done",
					Parts: []llm.AssistantPart{{
						Text: "done",
					}},
				}, nil
			}
		},
	}

	seen := []llm.Message{}
	session, err := NewSession(SessionConfig{
		Client:       client,
		Model:        "test-model",
		Tools:        []builtintools.Tool{echoTool{}},
		MaxTurns:     3,
		SystemPrompt: "sys",
		OnMessage: func(_ context.Context, msg llm.Message) error {
			seen = append(seen, msg)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	res, err := session.PromptWithOptions(context.Background(), "hello", PromptOptions{})
	if err != nil {
		t.Fatalf("PromptWithOptions: %v", err)
	}
	if res.FinalResponse != "done" {
		t.Fatalf("unexpected final response: %q", res.FinalResponse)
	}

	if len(seen) != 4 {
		t.Fatalf("expected 4 callback messages (user, assistant(tool_call), tool_result, assistant), got %d", len(seen))
	}
	if seen[0].Role != llm.RoleUser {
		t.Fatalf("callback[0] role = %s, want user", seen[0].Role)
	}
	if seen[1].Role != llm.RoleAssistant {
		t.Fatalf("callback[1] role = %s, want assistant", seen[1].Role)
	}
	if seen[2].Role != llm.RoleTool {
		t.Fatalf("callback[2] role = %s, want tool", seen[2].Role)
	}
	if seen[3].Role != llm.RoleAssistant {
		t.Fatalf("callback[3] role = %s, want assistant", seen[3].Role)
	}
}
