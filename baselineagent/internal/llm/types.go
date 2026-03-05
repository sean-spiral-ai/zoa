package llm

import "context"

type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

type ToolCall struct {
	ID               string
	Name             string
	Args             map[string]any
	ThoughtSignature string
}

type ToolResult struct {
	CallID  string
	Name    string
	Output  string
	IsError bool
}

type AssistantPart struct {
	Text             string
	ToolCall         *ToolCall
	ThoughtSignature string
}

type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

type Message struct {
	Role        MessageRole
	Text        string
	Parts       []AssistantPart
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

type CompletionRequest struct {
	Model            string
	Messages         []Message
	Tools            []ToolSpec
	Temperature      float64
	MaxOutputTokens  int
	ResponseMimeType string
	ResponseSchema   map[string]any
}

type CompletionResponse struct {
	Text      string
	Parts     []AssistantPart
	ToolCalls []ToolCall
}

type Client interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}
