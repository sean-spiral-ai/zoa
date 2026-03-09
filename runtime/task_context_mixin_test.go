package runtime

import (
	"context"
	"testing"

	baselineagent "zoa/baselineagent"
	"zoa/llm"
)

type stubConversation struct {
	history []llm.Message
}

func (s *stubConversation) Prompt(_ context.Context, _ string) (baselineagent.RunResult, error) {
	return baselineagent.RunResult{}, nil
}

func (s *stubConversation) PromptStructured(_ context.Context, _ string, _ llm.StructuredResponseFormat) (baselineagent.RunResult, error) {
	return baselineagent.RunResult{}, nil
}

func (s *stubConversation) AppendMessages(messages []llm.Message) error {
	s.history = append(s.history, messages...)
	return nil
}

func (s *stubConversation) Fork() baselineagent.Conversation {
	out := &stubConversation{
		history: append([]llm.Message(nil), s.history...),
	}
	return out
}

func (s *stubConversation) History() []llm.Message {
	return append([]llm.Message(nil), s.history...)
}

func TestTaskContextLoadMixinAppendsImmediately(t *testing.T) {
	conv := &stubConversation{}
	tc := &TaskContext{
		mainConv: conv,
		loadMixin: func(id string) (*Mixin, bool) {
			if id != "intrinsic.lmfunction_system" {
				return nil, false
			}
			return &Mixin{
				ID:        id,
				WhenToUse: "test",
				Content:   "important reference context",
			}, true
		},
	}

	if err := tc.LoadMixin("intrinsic.lmfunction_system"); err != nil {
		t.Fatalf("load mixin first: %v", err)
	}
	if err := tc.LoadMixin("intrinsic.lmfunction_system"); err != nil {
		t.Fatalf("load mixin second: %v", err)
	}
	if len(conv.history) != 2 {
		t.Fatalf("expected 2 appended messages, got %d", len(conv.history))
	}
	for i, msg := range conv.history {
		if msg.Role != llm.RoleUser {
			t.Fatalf("message[%d] role=%s, want user", i, msg.Role)
		}
		if msg.Text != "important reference context" {
			t.Fatalf("message[%d] text=%q, want mixin content", i, msg.Text)
		}
	}
}
