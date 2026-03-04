package llmtrace

import (
	"testing"
)

func TestNewStore(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Root sentinel should exist
	nodes, err := store.AllNodes()
	if err != nil {
		t.Fatalf("AllNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 root node, got %d", len(nodes))
	}
	if nodes[0].Hash != RootHash {
		t.Errorf("root hash = %q, want %q", nodes[0].Hash, RootHash)
	}
	if nodes[0].Role != "root" {
		t.Errorf("root role = %q, want %q", nodes[0].Role, "root")
	}
}

func TestInsertAndChildren(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	hash1, err := store.Insert(RootHash, "system", "Hello system", `{"Role":"system","Text":"Hello system"}`, "{}")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if hash1 == "" {
		t.Fatal("Insert returned empty hash")
	}

	hash2, err := store.Insert(hash1, "user", "Hello user", `{"Role":"user","Text":"Hello user"}`, "{}")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Children of root should be hash1
	children, err := store.Children(RootHash)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 1 || children[0].Hash != hash1 {
		t.Errorf("expected 1 child of root with hash %q, got %v", hash1, children)
	}

	// Children of hash1 should be hash2
	children, err = store.Children(hash1)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 1 || children[0].Hash != hash2 {
		t.Errorf("expected 1 child of hash1 with hash %q, got %v", hash2, children)
	}
}

func TestInsertIdempotent(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	msgJSON := `{"Role":"user","Text":"test"}`
	h1, err := store.Insert(RootHash, "user", "test", msgJSON, "{}")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	h2, err := store.Insert(RootHash, "user", "test", msgJSON, "{}")
	if err != nil {
		t.Fatalf("Insert duplicate: %v", err)
	}
	if h1 != h2 {
		t.Errorf("idempotent insert gave different hashes: %q vs %q", h1, h2)
	}

	// Should still be just root + 1 node
	nodes, err := store.AllNodes()
	if err != nil {
		t.Fatalf("AllNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes (root + 1), got %d", len(nodes))
	}
}

func TestSince(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	allNodes, _ := store.AllNodes()
	rootID := allNodes[0].ID

	store.Insert(RootHash, "system", "s1", `{"Role":"system","Text":"s1"}`, "{}")
	store.Insert(RootHash, "user", "u1", `{"Role":"user","Text":"u1"}`, "{}")

	nodes, err := store.Since(rootID)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes since root, got %d", len(nodes))
	}
}

func TestForkBranching(t *testing.T) {
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Create a shared parent
	parentHash, _ := store.Insert(RootHash, "system", "prompt", `{"Role":"system","Text":"prompt"}`, "{}")

	// Two different children from same parent = fork
	h1, _ := store.Insert(parentHash, "user", "branch A", `{"Role":"user","Text":"branch A"}`, "{}")
	h2, _ := store.Insert(parentHash, "user", "branch B", `{"Role":"user","Text":"branch B"}`, "{}")

	if h1 == h2 {
		t.Error("different messages from same parent should have different hashes")
	}

	children, _ := store.Children(parentHash)
	if len(children) != 2 {
		t.Errorf("expected 2 children (fork), got %d", len(children))
	}
}
