package lmfrt

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSearchLMFunctionsIncludesSchemas(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.schemas",
		WhenToUse: "test only",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean"},
			},
		},
		Exec: func(_ *TaskContext, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})

	tool := &searchLMFunctionsTool{registry: registry}
	out, err := tool.Execute(context.Background(), map[string]any{
		"query": "schemas",
		"limit": 10,
	})
	if err != nil {
		t.Fatalf("execute search_lmfunctions: %v", err)
	}

	var payload struct {
		Matches []struct {
			ID           string         `json:"id"`
			InputSchema  map[string]any `json:"input_schema"`
			OutputSchema map[string]any `json:"output_schema"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(payload.Matches))
	}
	match := payload.Matches[0]
	if match.ID != "test.schemas" {
		t.Fatalf("unexpected match id: %q", match.ID)
	}
	if match.InputSchema == nil {
		t.Fatalf("expected input_schema to be present")
	}
	if match.OutputSchema == nil {
		t.Fatalf("expected output_schema to be present")
	}
}
