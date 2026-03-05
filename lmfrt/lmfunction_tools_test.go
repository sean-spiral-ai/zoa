package lmfrt

import (
	"context"
	"encoding/json"
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

func TestCallLMFunctionDoesNotThreadTimeoutSec(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.call_lmfunction_no_timeout_field",
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
		"function_id": "test.call_lmfunction_no_timeout_field",
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

func TestCallLMFunctionSpecGuidesWaitAndKillAndOmitsTimeoutField(t *testing.T) {
	tool := &callLMFunctionTool{}
	spec := tool.Spec()
	if !strings.Contains(spec.Description, "wait_lmfunction") {
		t.Fatalf("call_lmfunction description should mention wait_lmfunction: %q", spec.Description)
	}
	if !strings.Contains(spec.Description, "kill_lmfunction") {
		t.Fatalf("call_lmfunction description should mention kill_lmfunction: %q", spec.Description)
	}
	props, _ := spec.Schema["properties"].(map[string]any)
	if _, ok := props["timeout_sec"]; ok {
		t.Fatalf("call_lmfunction schema should not expose timeout_sec")
	}
}

func TestKillLMFunctionCancelsRunningTask(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.kill_lmfunction.cancel",
		WhenToUse: "test only",
		Exec: func(tc *TaskContext, _ map[string]any) (map[string]any, error) {
			<-tc.Context().Done()
			return nil, tc.Context().Err()
		},
	})
	manager, err := NewTaskManager(registry, TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	callTool := &callLMFunctionTool{manager: manager}
	callOut, err := callTool.Execute(context.Background(), map[string]any{
		"function_id": "test.kill_lmfunction.cancel",
	})
	if err != nil {
		t.Fatalf("execute call_lmfunction: %v", err)
	}
	var callPayload struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(callOut), &callPayload); err != nil {
		t.Fatalf("decode call payload: %v", err)
	}
	if callPayload.TaskID == "" {
		t.Fatalf("missing task_id in call payload: %s", callOut)
	}

	killTool := &killLMFunctionTool{manager: manager}
	killOut, err := killTool.Execute(context.Background(), map[string]any{
		"task_id": callPayload.TaskID,
	})
	if err != nil {
		t.Fatalf("execute kill_lmfunction: %v", err)
	}
	var killPayload struct {
		CancelRequested bool `json:"cancel_requested"`
		Task            struct {
			TaskID string     `json:"task_id"`
			Status TaskStatus `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal([]byte(killOut), &killPayload); err != nil {
		t.Fatalf("decode kill payload: %v", err)
	}
	if !killPayload.CancelRequested {
		t.Fatalf("expected cancel_requested=true, payload=%s", killOut)
	}
	if killPayload.Task.TaskID != callPayload.TaskID {
		t.Fatalf("kill payload returned wrong task id: %q", killPayload.Task.TaskID)
	}

	snapshot, timedOut, err := manager.Wait(callPayload.TaskID, 2*time.Second)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if timedOut {
		t.Fatalf("wait timed out unexpectedly")
	}
	if snapshot.Status != TaskStatusCanceled {
		t.Fatalf("expected canceled status, got %s (%s)", snapshot.Status, snapshot.Error)
	}
}

func TestWaitLMFunctionSpecMentionsKillOnTimeout(t *testing.T) {
	tool := &waitLMFunctionTool{}
	spec := tool.Spec()
	if !strings.Contains(spec.Description, "kill_lmfunction") {
		t.Fatalf("wait_lmfunction description should mention kill_lmfunction: %q", spec.Description)
	}
	props, _ := spec.Schema["properties"].(map[string]any)
	timeoutProp, _ := props["timeout_sec"].(map[string]any)
	timeoutDesc, _ := timeoutProp["description"].(string)
	if !strings.Contains(strings.ToLower(timeoutDesc), "timed_out") {
		t.Fatalf("timeout_sec description should mention timed_out semantics: %q", timeoutDesc)
	}
}
