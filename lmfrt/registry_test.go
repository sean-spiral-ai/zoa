package lmfrt

import "testing"

func TestRegistryRejectsFunctionMixinIDCollision(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(&Function{
		ID:          "intrinsic.lmfunction_system",
		WhenToUse:   "test",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(_ *TaskContext, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})
	err := r.RegisterMixin(&Mixin{
		ID:        "intrinsic.lmfunction_system",
		WhenToUse: "test",
		Content:   "test mixin",
	})
	if err == nil {
		t.Fatalf("expected id collision error")
	}
}
