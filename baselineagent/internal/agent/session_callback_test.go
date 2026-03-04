package agent

import (
	"context"
	"testing"

	builtintools "zoa/baselineagent/builtintools"
	"zoa/baselineagent/internal/llm"
	"zoa/internal/llmtrace"
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

func TestTracerReceivesMessagesIncrementally(t *testing.T) {
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

	store, err := llmtrace.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	session, err := NewSession(SessionConfig{
		Client:       client,
		Model:        "test-model",
		Tools:        []builtintools.Tool{echoTool{}},
		MaxTurns:     3,
		SystemPrompt: "sys",
		Tracer:       llmtrace.NewTracer(store),
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

	// root + system + user + assistant(tool) + tool_result + assistant(done) = 6
	nodes, err := store.AllNodes()
	if err != nil {
		t.Fatalf("AllNodes: %v", err)
	}
	if len(nodes) != 6 {
		t.Fatalf("expected 6 nodes, got %d", len(nodes))
	}

	// Verify linear chain.
	for i := 1; i < len(nodes); i++ {
		if nodes[i].ParentHash != nodes[i-1].Hash {
			t.Errorf("node[%d].parent != node[%d].hash", i, i-1)
		}
	}
}
