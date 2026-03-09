package runtime

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
			TaskID:     "task-1",
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

func TestTaskLogStateMigrateFromLmfrt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	tc, err := NewTaskContext(context.Background(), TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	// Create the old lmfrt__task_log table and insert a record.
	if _, err := tc.SqlExec(`CREATE TABLE lmfrt__task_log (
		task_id TEXT PRIMARY KEY,
		function_id TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		output_json TEXT,
		error_text TEXT,
		hide_by_default INTEGER NOT NULL DEFAULT 0,
		input_json TEXT,
		conversation_json TEXT,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	if _, err := tc.SqlExec(`CREATE INDEX lmfrt__task_log_status_updated_idx ON lmfrt__task_log(status, updated_at DESC)`); err != nil {
		t.Fatalf("create old index: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tc.SqlExec(
		`INSERT INTO lmfrt__task_log(task_id, function_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"task-legacy", "test.legacy", string(TaskStatusDone), now, now,
	); err != nil {
		t.Fatalf("insert old record: %v", err)
	}

	// Now run Init(), which should migrate the table.
	state := LogState(tc)
	if err := state.Init(); err != nil {
		t.Fatalf("init (with migration): %v", err)
	}

	// Old table should be gone.
	rows, err := tc.SqlQuery(`SELECT name FROM sqlite_master WHERE type='table' AND name='lmfrt__task_log'`)
	if err != nil {
		t.Fatalf("check old table: %v", err)
	}
	if len(rows.Rows) != 0 {
		t.Fatalf("old lmfrt__task_log table should not exist after migration")
	}

	// New table should have the migrated record.
	got, err := state.Get("task-legacy")
	if err != nil {
		t.Fatalf("get migrated record: %v", err)
	}
	if got.FunctionID != "test.legacy" || got.Status != TaskStatusDone {
		t.Fatalf("unexpected migrated record: %#v", got.TaskSnapshot)
	}

	// Old index should be gone, new index should exist.
	idxRows, err := tc.SqlQuery(`SELECT name FROM sqlite_master WHERE type='index' AND name='lmfrt__task_log_status_updated_idx'`)
	if err != nil {
		t.Fatalf("check old index: %v", err)
	}
	if len(idxRows.Rows) != 0 {
		t.Fatalf("old index should not exist after migration")
	}
	newIdxRows, err := tc.SqlQuery(`SELECT name FROM sqlite_master WHERE type='index' AND name='runtime__task_log_status_updated_idx'`)
	if err != nil {
		t.Fatalf("check new index: %v", err)
	}
	if len(newIdxRows.Rows) != 1 {
		t.Fatalf("new index should exist after migration")
	}
}

func TestTaskLogStateMigrateSkipsWhenBothExist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	tc, err := NewTaskContext(context.Background(), TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	// Create both old and new tables.
	for _, table := range []string{"lmfrt__task_log", "runtime__task_log"} {
		if _, err := tc.SqlExec(`CREATE TABLE "` + table + `" (
			task_id TEXT PRIMARY KEY,
			function_id TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			started_at TEXT,
			finished_at TEXT,
			output_json TEXT,
			error_text TEXT,
			hide_by_default INTEGER NOT NULL DEFAULT 0,
			input_json TEXT,
			conversation_json TEXT,
			updated_at TEXT NOT NULL
		)`); err != nil {
			t.Fatalf("create table %s: %v", table, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Put a record in the NEW table.
	if _, err := tc.SqlExec(
		`INSERT INTO runtime__task_log(task_id, function_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"task-new", "test.new", string(TaskStatusDone), now, now,
	); err != nil {
		t.Fatalf("insert new record: %v", err)
	}

	// Init should NOT clobber the new table with the old one.
	state := LogState(tc)
	if err := state.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	got, err := state.Get("task-new")
	if err != nil {
		t.Fatalf("get new record: %v", err)
	}
	if got.FunctionID != "test.new" {
		t.Fatalf("new table record should be preserved, got: %#v", got.TaskSnapshot)
	}
}

func TestTaskLogStateGet(t *testing.T) {
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
	startedAt := now.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	record := TaskLogRecord{
		TaskSnapshot: TaskSnapshot{
			TaskID:     "task-get",
			FunctionID: "test.get",
			Status:     TaskStatusDone,
			CreatedAt:  now,
			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
			Output:     map[string]any{"ok": true},
		},
		Input:         map[string]any{"name": "value"},
		HideByDefault: true,
		UpdatedAt:     finishedAt,
	}
	if err := state.upsert(record); err != nil {
		t.Fatalf("upsert record: %v", err)
	}

	got, err := state.Get(record.TaskID)
	if err != nil {
		t.Fatalf("get record: %v", err)
	}
	if got.TaskID != record.TaskID || got.FunctionID != record.FunctionID || got.Status != TaskStatusDone {
		t.Fatalf("unexpected snapshot: %#v", got.TaskSnapshot)
	}
	if got.Output["ok"] != true {
		t.Fatalf("unexpected output: %#v", got.Output)
	}
	if got.Input["name"] != "value" {
		t.Fatalf("unexpected input: %#v", got.Input)
	}
	if !got.HideByDefault {
		t.Fatalf("expected hide_by_default=true")
	}
}
