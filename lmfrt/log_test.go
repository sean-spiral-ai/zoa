package lmfrt

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskLogStateSanity(t *testing.T) {
	tc, err := NewTaskContext(context.Background(), TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	state := LogState(tc)
	if err := state.Init(); err != nil {
		t.Fatalf("init task log state: %v", err)
	}

	now := time.Now().UTC()
	running := TaskLogRecord{
		TaskSnapshot: TaskSnapshot{
			TaskID:     "task-000001",
			FunctionID: "test.fn",
			Status:     TaskStatusRunning,
			CreatedAt:  now,
		},
		Input:     map[string]any{"k": "v"},
		UpdatedAt: now,
	}
	if err := state.upsert(running); err != nil {
		t.Fatalf("upsert running record: %v", err)
	}

	runningItems, err := state.Summaries(10, true, true)
	if err != nil {
		t.Fatalf("list running summaries: %v", err)
	}
	if len(runningItems) != 1 {
		t.Fatalf("expected 1 running summary, got %d", len(runningItems))
	}
	if runningItems[0].TaskID != running.TaskID || runningItems[0].Status != TaskStatusRunning {
		t.Fatalf("unexpected running summary: %#v", runningItems[0])
	}

	finishedAt := now.Add(2 * time.Second)
	done := running
	done.Status = TaskStatusDone
	done.FinishedAt = &finishedAt
	done.Output = map[string]any{"ok": true}
	done.UpdatedAt = finishedAt
	if err := state.upsert(done); err != nil {
		t.Fatalf("upsert done record: %v", err)
	}

	afterDoneRunning, err := state.Summaries(10, true, true)
	if err != nil {
		t.Fatalf("list running summaries after done: %v", err)
	}
	if len(afterDoneRunning) != 0 {
		t.Fatalf("expected 0 running summaries after done, got %d", len(afterDoneRunning))
	}

	all, err := state.Summaries(10, false, true)
	if err != nil {
		t.Fatalf("list all summaries: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(all))
	}
	if all[0].TaskID != done.TaskID || all[0].FunctionID != done.FunctionID || all[0].Status != TaskStatusDone {
		t.Fatalf("unexpected final summary: %#v", all[0])
	}
}

func TestTaskLogStateHideByDefaultFilter(t *testing.T) {
	tc, err := NewTaskContext(context.Background(), TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	state := LogState(tc)
	if err := state.Init(); err != nil {
		t.Fatalf("init task log state: %v", err)
	}

	now := time.Now().UTC()
	if err := state.upsert(TaskLogRecord{
		TaskSnapshot: TaskSnapshot{
			TaskID:     "task-visible",
			FunctionID: "test.visible",
			Status:     TaskStatusDone,
			CreatedAt:  now,
		},
		HideByDefault: false,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("upsert visible: %v", err)
	}
	if err := state.upsert(TaskLogRecord{
		TaskSnapshot: TaskSnapshot{
			TaskID:     "task-hidden",
			FunctionID: "test.hidden",
			Status:     TaskStatusDone,
			CreatedAt:  now,
		},
		HideByDefault: true,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("upsert hidden: %v", err)
	}

	visibleOnly, err := state.Summaries(10, false, false)
	if err != nil {
		t.Fatalf("summaries visible only: %v", err)
	}
	if len(visibleOnly) != 1 || visibleOnly[0].TaskID != "task-visible" {
		t.Fatalf("unexpected visible summaries: %#v", visibleOnly)
	}

	withHidden, err := state.Summaries(10, false, true)
	if err != nil {
		t.Fatalf("summaries include hidden: %v", err)
	}
	if len(withHidden) != 2 {
		t.Fatalf("expected 2 summaries with hidden included, got %d", len(withHidden))
	}
}
