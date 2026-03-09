package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskManagerCancelDoneTaskReturnsFalse(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.cancel.done",
		WhenToUse: "test only",
		Exec: func(_ *TaskContext, _ map[string]any) (map[string]any, error) {
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

	taskID, err := manager.Spawn("test.cancel.done", map[string]any{}, SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}
	if _, timedOut, err := manager.Wait(taskID, 2*time.Second); err != nil {
		t.Fatalf("wait task: %v", err)
	} else if timedOut {
		t.Fatalf("wait timed out unexpectedly")
	}

	cancelRequested, err := manager.Cancel(taskID)
	if err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	if cancelRequested {
		t.Fatalf("expected cancel_requested=false for done task")
	}
}

func TestTaskManagerCompletedTaskRemovedFromMemory(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.completed.cleanup",
		WhenToUse: "test only",
		Exec: func(_ *TaskContext, _ map[string]any) (map[string]any, error) {
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

	taskID, err := manager.Spawn("test.completed.cleanup", map[string]any{}, SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}
	snapshot, timedOut, err := manager.Wait(taskID, 2*time.Second)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if timedOut {
		t.Fatalf("wait timed out unexpectedly")
	}
	if snapshot.Status != TaskStatusDone {
		t.Fatalf("expected done status, got %s", snapshot.Status)
	}

	mgr := manager
	mgr.mu.RLock()
	_, ok := mgr.tasks[taskID]
	mgr.mu.RUnlock()
	if ok {
		t.Fatalf("expected completed task to be removed from in-memory map")
	}

	got, err := manager.Get(taskID)
	if err != nil {
		t.Fatalf("get task after cleanup: %v", err)
	}
	if got.Status != TaskStatusDone {
		t.Fatalf("expected persisted done status, got %s", got.Status)
	}
	if got.Output["ok"] != true {
		t.Fatalf("unexpected output after cleanup: %#v", got.Output)
	}
}

func TestTaskManagerCancelUnknownTask(t *testing.T) {
	manager, err := NewTaskManager(NewRegistry(), TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	_, err = manager.Cancel("task-does-not-exist")
	if err == nil {
		t.Fatalf("expected unknown task error")
	}
}

func TestTaskManagerCancelPropagatesContextCancel(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&Function{
		ID:        "test.cancel.running",
		WhenToUse: "test only",
		Exec: func(tc *TaskContext, _ map[string]any) (map[string]any, error) {
			select {
			case <-tc.Context().Done():
				return nil, tc.Context().Err()
			case <-time.After(5 * time.Second):
				return map[string]any{"ok": true}, nil
			}
		},
	})
	manager, err := NewTaskManagerWithContext(context.Background(), registry, TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	taskID, err := manager.Spawn("test.cancel.running", map[string]any{}, SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}
	cancelRequested, err := manager.Cancel(taskID)
	if err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	if !cancelRequested {
		t.Fatalf("expected cancel_requested=true")
	}

	snapshot, timedOut, err := manager.Wait(taskID, 2*time.Second)
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

func TestTaskManagerCancelCascadesToChildTasks(t *testing.T) {
	registry := NewRegistry()
	childStarted := make(chan string, 1)
	registry.MustRegister(&Function{
		ID:        "test.cancel.child",
		WhenToUse: "test only",
		Exec: func(tc *TaskContext, _ map[string]any) (map[string]any, error) {
			<-tc.Context().Done()
			return nil, tc.Context().Err()
		},
	})
	registry.MustRegister(&Function{
		ID:        "test.cancel.parent",
		WhenToUse: "test only",
		Exec: func(tc *TaskContext, _ map[string]any) (map[string]any, error) {
			childID, err := tc.Spawn("test.cancel.child", map[string]any{}, SpawnOptions{})
			if err != nil {
				return nil, err
			}
			select {
			case childStarted <- childID:
			default:
			}
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

	parentID, err := manager.Spawn("test.cancel.parent", map[string]any{}, SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn parent task: %v", err)
	}
	var childID string
	select {
	case childID = <-childStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for child spawn")
	}
	if childID == "" {
		t.Fatalf("expected non-empty child task id")
	}

	cancelRequested, err := manager.Cancel(parentID)
	if err != nil {
		t.Fatalf("cancel parent task: %v", err)
	}
	if !cancelRequested {
		t.Fatalf("expected cancel_requested=true")
	}

	parentSnap, parentTimedOut, err := manager.Wait(parentID, 2*time.Second)
	if err != nil {
		t.Fatalf("wait parent task: %v", err)
	}
	if parentTimedOut {
		t.Fatalf("wait parent timed out unexpectedly")
	}
	if parentSnap.Status != TaskStatusCanceled {
		t.Fatalf("expected parent canceled status, got %s (%s)", parentSnap.Status, parentSnap.Error)
	}

	childSnap, childTimedOut, err := manager.Wait(childID, 2*time.Second)
	if err != nil {
		t.Fatalf("wait child task: %v", err)
	}
	if childTimedOut {
		t.Fatalf("wait child timed out unexpectedly")
	}
	if childSnap.Status != TaskStatusCanceled {
		t.Fatalf("expected child canceled status, got %s (%s)", childSnap.Status, childSnap.Error)
	}
}
