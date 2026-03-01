package baselineagent

import (
	"encoding/json"

	"codexagentbase/baselineagent/internal/llm"
)

type ConversationRole = llm.MessageRole

const (
	RoleSystem    ConversationRole = llm.RoleSystem
	RoleUser      ConversationRole = llm.RoleUser
	RoleAssistant ConversationRole = llm.RoleAssistant
	RoleTool      ConversationRole = llm.RoleTool
)

type ConversationToolCall = llm.ToolCall
type ConversationToolResult = llm.ToolResult
type ConversationAssistantPart = llm.AssistantPart
type ConversationMessage = llm.Message

func toLLMMessages(in []ConversationMessage) []llm.Message {
	return cloneConversationMessages(in)
}

func fromLLMMessages(in []llm.Message) []ConversationMessage {
	return cloneConversationMessages(in)
}

func cloneConversationMessages[T ~[]llm.Message](in T) T {
	if in == nil {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make(T, len(in))
		copy(out, in)
		return out
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		out = make(T, len(in))
		copy(out, in)
	}
	return out
}
