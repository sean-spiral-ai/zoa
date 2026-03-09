package llm

import toplevelllm "zoa/llm"

type ToolSpec = toplevelllm.ToolSpec
type ToolCall = toplevelllm.ToolCall
type ToolResult = toplevelllm.ToolResult
type AssistantPart = toplevelllm.AssistantPart
type MessageRole = toplevelllm.MessageRole

const (
	RoleSystem    = toplevelllm.RoleSystem
	RoleUser      = toplevelllm.RoleUser
	RoleAssistant = toplevelllm.RoleAssistant
	RoleTool      = toplevelllm.RoleTool
)

type Message = toplevelllm.Message
type CompletionRequest = toplevelllm.CompletionRequest
type CompletionResponse = toplevelllm.CompletionResponse
type Client = toplevelllm.Client
