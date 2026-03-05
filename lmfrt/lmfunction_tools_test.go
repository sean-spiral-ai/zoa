package lmfrt

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		"keywords": []any{"schemas"},
		"limit":    10,
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

func TestSearchLMMixinFindsRegisteredMixin(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegisterMixin(&Mixin{
		ID:        "intrinsic.lmfunction_system",
		WhenToUse: "reference context",
		Content:   "mixin text",
	})

	tool := &searchLMMixinsTool{registry: registry}
	out, err := tool.Execute(context.Background(), map[string]any{
		"keywords": []any{"does-not-match", "lmfunction"},
		"limit":    10,
	})
	if err != nil {
		t.Fatalf("execute search_lmmixin: %v", err)
	}

	var payload struct {
		Matches []struct {
			ID string `json:"id"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(payload.Matches))
	}
	if payload.Matches[0].ID != "intrinsic.lmfunction_system" {
		t.Fatalf("unexpected match id: %q", payload.Matches[0].ID)
	}
}

func TestLoadLMMixinLoadsIntoContext(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegisterMixin(&Mixin{
		ID:        "intrinsic.lmfunction_system",
		WhenToUse: "reference context",
		Content:   "mixin text",
	})

	tool := &loadLMMixinTool{
		registry: registry,
	}
	out, err := tool.Execute(context.Background(), map[string]any{
		"mixin_id": "intrinsic.lmfunction_system",
	})
	if err != nil {
		t.Fatalf("execute load_lmmixin: %v", err)
	}
	if out != "mixin text" {
		t.Fatalf("unexpected mixin output: %q", out)
	}
}

func TestCallLMFunctionSupportsTaskTimeout(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.call_lmfunction_timeout",
		WhenToUse: "test only",
		Exec: func(_ *TaskContext, input map[string]any) (map[string]any, error) {
			_, hasTimeout := input["timeout_sec"]
			return map[string]any{
				"saw_timeout_in_input": hasTimeout,
			}, nil
		},
	})
	manager, err := NewTaskManager(registry, TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	tool := &callLMFunctionTool{manager: manager}
	out, err := tool.Execute(context.Background(), map[string]any{
		"function_id": "test.call_lmfunction_timeout",
		"input": map[string]any{
			"foo": "bar",
		},
		"timeout_sec": 1,
	})
	if err != nil {
		t.Fatalf("execute call_lmfunction: %v", err)
	}

	var payload struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.TaskID == "" {
		t.Fatalf("missing task_id in payload: %s", out)
	}

	snapshot, timedOut, err := manager.Wait(payload.TaskID, 3*time.Second)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if timedOut {
		t.Fatalf("wait timed out unexpectedly")
	}
	if snapshot.Status != TaskStatusDone {
		t.Fatalf("expected done status, got %s (%s)", snapshot.Status, snapshot.Error)
	}
	if saw, _ := snapshot.Output["saw_timeout_in_input"].(bool); saw {
		t.Fatalf("timeout_sec leaked into function input")
	}
}

func TestCallLMFunctionRejectsNegativeTimeout(t *testing.T) {
	tool := &callLMFunctionTool{}
	_, err := tool.Execute(context.Background(), map[string]any{
		"function_id": "test.any",
		"timeout_sec": -1,
	})
	if err == nil {
		t.Fatalf("expected timeout validation error")
	}
}

func TestCallLMFunctionTimeoutAppliesToWholeTaskDuration(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.call_lmfunction_whole_task_timeout",
		WhenToUse: "test only",
		Exec: func(tc *TaskContext, _ map[string]any) (map[string]any, error) {
			ctx := tc.Context()
			if _, ok := ctx.Deadline(); !ok {
				return nil, fmt.Errorf("missing task deadline")
			}
			step := func(d time.Duration) error {
				timer := time.NewTimer(d)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			}
			if err := step(650 * time.Millisecond); err != nil {
				return nil, err
			}
			if err := step(650 * time.Millisecond); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	})
	manager, err := NewTaskManager(registry, TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	tool := &callLMFunctionTool{manager: manager}
	out, err := tool.Execute(context.Background(), map[string]any{
		"function_id": "test.call_lmfunction_whole_task_timeout",
		"timeout_sec": 1,
	})
	if err != nil {
		t.Fatalf("execute call_lmfunction: %v", err)
	}

	var payload struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.TaskID == "" {
		t.Fatalf("missing task_id in payload: %s", out)
	}

	snapshot, timedOut, err := manager.Wait(payload.TaskID, 5*time.Second)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if timedOut {
		t.Fatalf("wait timed out unexpectedly")
	}
	if snapshot.Status != TaskStatusFailed {
		t.Fatalf("expected failed status, got %s", snapshot.Status)
	}
	if !strings.Contains(strings.ToLower(snapshot.Error), "deadline exceeded") {
		t.Fatalf("expected deadline exceeded error, got: %q", snapshot.Error)
	}
}
