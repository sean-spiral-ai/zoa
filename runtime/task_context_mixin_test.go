package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"zoa/llm"
)

func TestTaskContextLoadMixinAppendsImmediately(t *testing.T) {
	tc, err := NewTaskContext(context.Background(), TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
		loadMixin: func(id string) (*Mixin, bool) {
			if id != "intrinsic.zoa_system" {
				return nil, false
			}
			return &Mixin{
				ID:        id,
				WhenToUse: "test",
				Content:   "important reference context",
			}, true
		},
	})
	if err != nil {
		t.Fatalf("new task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	if err := tc.LoadMixin("intrinsic.zoa_system"); err != nil {
		t.Fatalf("load mixin first: %v", err)
	}
	if err := tc.LoadMixin("intrinsic.zoa_system"); err != nil {
		t.Fatalf("load mixin second: %v", err)
	}
	history := tc.conversationHistory()
	if len(history) != 3 {
		t.Fatalf("expected 3 messages including system prompt, got %d", len(history))
	}
	for i, msg := range history[1:] {
		if msg.Role != llm.RoleUser {
			t.Fatalf("message[%d] role=%s, want user", i, msg.Role)
		}
		if msg.Text != "important reference context" {
			t.Fatalf("message[%d] text=%q, want mixin content", i, msg.Text)
		}
	}
}
