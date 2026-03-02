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

	runningItems, err := state.Summaries(10, true)
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

	afterDoneRunning, err := state.Summaries(10, true)
	if err != nil {
		t.Fatalf("list running summaries after done: %v", err)
	}
	if len(afterDoneRunning) != 0 {
		t.Fatalf("expected 0 running summaries after done, got %d", len(afterDoneRunning))
	}

	all, err := state.Summaries(10, false)
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
