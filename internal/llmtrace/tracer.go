package llmtrace

import (
	"encoding/json"
	"sync"
)

// MessageTracer is the interface for tracing LLM conversation trees.
type MessageTracer interface {
	OnMessage(msg any, metadata map[string]any) error
	Fork() MessageTracer
}

// Tracer tracks the head of a single conversation branch within a Store.
type Tracer struct {
	store *Store
	mu    sync.Mutex
	head  string
}

// NewTracer creates a tracer rooted at the root sentinel.
func NewTracer(store *Store) *Tracer {
	return &Tracer{
		store: store,
		head:  RootHash,
	}
}

// OnMessage computes the content-addressable hash for msg, inserts it into the
// store as a child of the current head, and advances head to the new node.
func (t *Tracer) OnMessage(msg any, metadata map[string]any) error {
	msgJSON, err := CanonicalJSON(msg)
	if err != nil {
		return err
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		metaJSON = []byte("{}")
	}

	var parsed map[string]any
	_ = json.Unmarshal(msgJSON, &parsed)
	role, _ := parsed["Role"].(string)
	text, _ := parsed["Text"].(string)
	var toolCalls []any
	if tc, ok := parsed["ToolCalls"].([]any); ok {
		toolCalls = tc
	}
	var toolResults []any
	if tr, ok := parsed["ToolResults"].([]any); ok {
		toolResults = tr
	}
	summary := MessageSummary(role, text, toolCalls, toolResults)

	t.mu.Lock()
	defer t.mu.Unlock()

	hash, err := t.store.Insert(t.head, role, summary, string(msgJSON), string(metaJSON))
	if err != nil {
		return err
	}
	t.head = hash
	return nil
}

// Fork returns a new Tracer sharing the same store but with an independent head
// starting at the current position.
func (t *Tracer) Fork() MessageTracer {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &Tracer{
		store: t.store,
		head:  t.head,
	}
}

// Head returns the current head hash.
func (t *Tracer) Head() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.head
}
