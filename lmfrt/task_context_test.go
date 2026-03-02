package lmfrt_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baselineagent "zoa/baselineagent"
	lmfrt "zoa/lmfrt"
)

func TestProgrammaticGuard(t *testing.T) {
	guard := &lmfrt.Function{
		ID:          "test.local.programmatic_guard",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(_ *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			raw, ok := input["value"]
			if !ok {
				return nil, fmt.Errorf("missing value")
			}

			var value int
			switch n := raw.(type) {
			case int:
				value = n
			case float64:
				value = int(n)
			default:
				return nil, fmt.Errorf("value must be number")
			}

			if value <= 0 {
				return nil, fmt.Errorf("value must be > 0")
			}
			return map[string]any{"accepted_value": value}, nil
		},
	}

	t.Run("fails for non-positive", func(t *testing.T) {
		registry := lmfrt.NewRegistry()
		registry.MustRegister(guard)
		manager, err := lmfrt.NewTaskManagerWithContext(context.Background(), registry, lmfrt.TaskManagerOptions{
			SQLitePath: filepath.Join(t.TempDir(), "state.db"),
		})
		if err != nil {
			t.Fatalf("create task manager: %v", err)
		}
		defer func() { _ = manager.Close() }()
		_, err = runTaskAndWait(manager, guard.ID, map[string]any{"value": 0})
		if err == nil {
			t.Fatalf("expected error for non-positive value")
		}
	})

	t.Run("passes for positive", func(t *testing.T) {
		registry := lmfrt.NewRegistry()
		registry.MustRegister(guard)
		manager, err := lmfrt.NewTaskManagerWithContext(context.Background(), registry, lmfrt.TaskManagerOptions{
			SQLitePath: filepath.Join(t.TempDir(), "state.db"),
		})
		if err != nil {
			t.Fatalf("create task manager: %v", err)
		}
		defer func() { _ = manager.Close() }()
		res, err := runTaskAndWait(manager, guard.ID, map[string]any{"value": 5})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, ok := res.Output["accepted_value"]
		if !ok || got != 5 {
			t.Fatalf("unexpected output: %#v", res.Output)
		}
	})
}

func TestNLExecContextMemory(t *testing.T) {
	model := requireModel(t)
	tc := newTaskContext(t, model)

	first, err := lmfrt.NLExecTyped[int](tc, "Return the number 7 as JSON number only.", nil)
	if err != nil {
		t.Fatalf("first nlexec failed: %v", err)
	}

	second, err := lmfrt.NLExecTyped[int](tc, "What number did you just return? Return JSON number only.", nil)
	if err != nil {
		t.Fatalf("second nlexec failed: %v", err)
	}

	if first != 7 {
		t.Fatalf("unexpected first value: %d", first)
	}
	if second != first {
		t.Fatalf("context memory mismatch: first=%d second=%d", first, second)
	}
}

func TestNLConditionIsolation(t *testing.T) {
	model := requireModel(t)
	tc := newTaskContext(t, model)

	first, err := lmfrt.NLExecTyped[int](tc, "Return the number 7 as JSON number only.", nil)
	if err != nil {
		t.Fatalf("first nlexec failed: %v", err)
	}
	if first != 7 {
		t.Fatalf("unexpected first value: %d", first)
	}

	err = tc.NLCondition(
		"branch_probe",
		"For this isolated branch, set passed=true and explanation='branch remembered 42'.",
		nil,
	)
	if err != nil {
		t.Fatalf("condition failed unexpectedly: %v", err)
	}

	second, err := lmfrt.NLExecTyped[int](tc, "What number did you just return earlier? Return JSON number only.", nil)
	if err != nil {
		t.Fatalf("second nlexec failed: %v", err)
	}
	if second != first {
		t.Fatalf("isolation mismatch: first=%d second=%d", first, second)
	}
}

func TestNLConditionFailure(t *testing.T) {
	model := requireModel(t)
	tc := newTaskContext(t, model)

	err := tc.NLCondition(
		"contains_token",
		"Set passed=true only when candidate_text contains the exact token BANANA_SENTINEL; otherwise passed=false.",
		map[string]any{"candidate_text": "Install dependencies and run go test."},
	)
	if err == nil {
		t.Fatalf("expected NL condition failure")
	}

	var condErr *lmfrt.NLConditionError
	if !asConditionErr(err, &condErr) {
		t.Fatalf("expected NLConditionError, got: %T %v", err, err)
	}
	if condErr.ConditionID != "contains_token" {
		t.Fatalf("unexpected condition id: %q", condErr.ConditionID)
	}
}

func TestTypedNLExecEcho(t *testing.T) {
	model := requireModel(t)
	tc := newTaskContext(t, model)

	type greeting struct {
		Message string `json:"message"`
	}

	var out greeting
	err := tc.NLExecTyped(
		"Return a JSON object with field 'message' greeting the provided name.",
		map[string]any{"name": "sean"},
		&out,
	)
	if err != nil {
		t.Fatalf("typed nlexec failed: %v", err)
	}
	if strings.TrimSpace(out.Message) == "" {
		t.Fatalf("typed response missing message: %#v", out)
	}
	if !strings.Contains(strings.ToLower(out.Message), "sean") {
		t.Fatalf("message does not include name: %q", out.Message)
	}
}

func requireModel(t *testing.T) string {
	t.Helper()
	if _, ok := baselineagent.ResolveCredential("", baselineagent.DefaultGeminiModel); ok {
		return baselineagent.DefaultGeminiModel
	}
	if _, ok := baselineagent.ResolveCredential("", baselineagent.DefaultAnthropicModel); ok {
		return baselineagent.DefaultAnthropicModel
	}
	t.Fatalf(
		"%s or %s is required to run LLM-backed tests",
		baselineagent.GeminiAPIKeyEnvVar,
		baselineagent.AnthropicOAuthTokenEnvVar,
	)
	return ""
}

func newTaskContext(t *testing.T, model string) *lmfrt.TaskContext {
	t.Helper()
	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:         t.TempDir(),
		Model:       model,
		MaxTurns:    24,
		Timeout:     120 * time.Second,
		Temperature: 0,
		SQLitePath:  filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	return tc
}

func asConditionErr(err error, target **lmfrt.NLConditionError) bool {
	return errors.As(err, target)
}

func runTaskAndWait(manager *lmfrt.TaskManager, functionID string, input map[string]any) (lmfrt.TaskSnapshot, error) {
	taskID, err := manager.Spawn(functionID, input, lmfrt.SpawnOptions{})
	if err != nil {
		return lmfrt.TaskSnapshot{}, err
	}
	snapshot, _, err := manager.Wait(taskID, 0)
	if err != nil {
		return lmfrt.TaskSnapshot{}, err
	}
	if snapshot.Status == lmfrt.TaskStatusFailed {
		if strings.TrimSpace(snapshot.Error) == "" {
			return lmfrt.TaskSnapshot{}, fmt.Errorf("task %s failed", taskID)
		}
		return lmfrt.TaskSnapshot{}, fmt.Errorf("%s", snapshot.Error)
	}
	if snapshot.Status != lmfrt.TaskStatusDone {
		return lmfrt.TaskSnapshot{}, fmt.Errorf("task %s ended in unexpected status %s", taskID, snapshot.Status)
	}
	return snapshot, nil
}
