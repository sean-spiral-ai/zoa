package llmtrace

import (
	"testing"
)

func TestTracerOnMessage(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	tracer := NewTracer(store)
	if tracer.Head() != RootHash {
		t.Errorf("initial head = %q, want root hash", tracer.Head())
	}

	msg := map[string]any{"Role": "system", "Text": "You are helpful."}
	if err := tracer.OnMessage(msg, map[string]any{"model": "test"}); err != nil {
		t.Fatalf("OnMessage: %v", err)
	}

	head1 := tracer.Head()
	if head1 == RootHash {
		t.Error("head should have advanced from root")
	}

	msg2 := map[string]any{"Role": "user", "Text": "Hello"}
	if err := tracer.OnMessage(msg2, nil); err != nil {
		t.Fatalf("OnMessage: %v", err)
	}

	head2 := tracer.Head()
	if head2 == head1 {
		t.Error("head should have advanced after second message")
	}

	nodes, _ := store.AllNodes()
	if len(nodes) != 3 { // root + system + user
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
}

func TestTracerFork(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	tracer := NewTracer(store)

	msg := map[string]any{"Role": "system", "Text": "prompt"}
	tracer.OnMessage(msg, nil)
	forkPoint := tracer.Head()

	forked := tracer.Fork()
	forkedTracer := forked.(*Tracer)
	if forkedTracer.Head() != forkPoint {
		t.Error("forked tracer should start at fork point")
	}

	tracer.OnMessage(map[string]any{"Role": "user", "Text": "branch A"}, nil)
	forked.OnMessage(map[string]any{"Role": "user", "Text": "branch B"}, nil)

	if tracer.Head() == forkedTracer.Head() {
		t.Error("branches should have different heads")
	}

	children, _ := store.Children(forkPoint)
	if len(children) != 2 {
		t.Errorf("expected 2 children at fork point, got %d", len(children))
	}
}
