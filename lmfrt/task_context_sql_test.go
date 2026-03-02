package lmfrt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	lmfrt "zoa/lmfrt"
)

func TestTaskContextSqlExecQueryBasicCRUD(t *testing.T) {
	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	if _, err := tc.SqlExec(`CREATE TABLE test__items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	insertRes, err := tc.SqlExec(`INSERT INTO test__items(name) VALUES (?)`, "alpha")
	if err != nil {
		t.Fatalf("insert row: %v", err)
	}
	if insertRes.RowsAffected != 1 {
		t.Fatalf("expected RowsAffected=1, got %d", insertRes.RowsAffected)
	}

	queryRes, err := tc.SqlQuery(`SELECT id, name FROM test__items ORDER BY id`)
	if err != nil {
		t.Fatalf("select rows: %v", err)
	}
	if len(queryRes.Columns) != 2 || queryRes.Columns[0] != "id" || queryRes.Columns[1] != "name" {
		t.Fatalf("unexpected columns: %#v", queryRes.Columns)
	}
	if len(queryRes.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(queryRes.Rows))
	}
	if got := queryRes.Rows[0]["name"]; got != "alpha" {
		t.Fatalf("unexpected row name: %#v", got)
	}
}

func TestTaskContextSqlTxRollbackOnError(t *testing.T) {
	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	if _, err := tc.SqlExec(`CREATE TABLE test__events(id INTEGER PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	txErr := tc.SqlTx(func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(tc.Context(), `INSERT INTO test__events(body) VALUES (?)`, "first"); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if txErr == nil {
		t.Fatalf("expected rollback error")
	}

	queryRes, err := tc.SqlQuery(`SELECT COUNT(*) AS c FROM test__events`)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if len(queryRes.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(queryRes.Rows))
	}
	if count, ok := queryRes.Rows[0]["c"].(int64); !ok || count != 0 {
		t.Fatalf("expected count 0, got %#v", queryRes.Rows[0]["c"])
	}
}

func TestNewTaskContextRequiresSQLite(t *testing.T) {
	_, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD: t.TempDir(),
	})
	if err == nil {
		t.Fatalf("expected sqlite required error")
	}
}

func TestTaskManagerSpawnUsesSQLitePath(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.sql.counter",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			if _, err := tc.SqlExec(`CREATE TABLE IF NOT EXISTS test__counter(value INTEGER NOT NULL)`); err != nil {
				return nil, err
			}
			if _, err := tc.SqlExec(`INSERT INTO test__counter(value) VALUES (1)`); err != nil {
				return nil, err
			}
			res, err := tc.SqlQuery(`SELECT COUNT(*) AS c FROM test__counter`)
			if err != nil {
				return nil, err
			}
			return map[string]any{"count": res.Rows[0]["c"]}, nil
		},
	})

	manager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	first, err := runTaskAndWaitSnapshot(manager, "test.sql.counter", nil, lmfrt.SpawnOptions{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := runTaskAndWaitSnapshot(manager, "test.sql.counter", nil, lmfrt.SpawnOptions{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if firstCount, ok := first.Output["count"].(int64); !ok || firstCount != 1 {
		t.Fatalf("unexpected first count: %#v", first.Output["count"])
	}
	if secondCount, ok := second.Output["count"].(int64); !ok || secondCount != 2 {
		t.Fatalf("unexpected second count: %#v", second.Output["count"])
	}
}

func TestTaskManagerInitRunsInitFunctions(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.__init__",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			if _, err := tc.SqlExec(`CREATE TABLE IF NOT EXISTS test__init_runs(id INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
				return nil, err
			}
			if _, err := tc.SqlExec(`INSERT INTO test__init_runs DEFAULT VALUES`); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	})
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.noop",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(_ *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})
	registry.MustRegister(&lmfrt.Function{
		ID:          "ztest.__init__",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			if _, err := tc.SqlExec(`INSERT INTO test__init_runs DEFAULT VALUES`); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	})

	manager, err := lmfrt.NewTaskManagerWithContext(context.Background(), registry, lmfrt.TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	if err := manager.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	checkFn := &lmfrt.Function{
		ID:          "test.count_init_runs",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			rows, err := tc.SqlQuery(`SELECT COUNT(*) AS c FROM test__init_runs`)
			if err != nil {
				return nil, err
			}
			return map[string]any{"count": rows.Rows[0]["c"]}, nil
		},
	}
	registry.MustRegister(checkFn)

	countRes, err := runTaskAndWaitSnapshot(manager, checkFn.ID, map[string]any{}, lmfrt.SpawnOptions{})
	if err != nil {
		t.Fatalf("run count: %v", err)
	}
	if count, ok := countRes.Output["count"].(int64); !ok || count != 2 {
		t.Fatalf("unexpected init run count: %#v", countRes.Output["count"])
	}
}

func TestTaskManagerInitReturnsError(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.__init__",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(_ *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			return nil, fmt.Errorf("boom")
		},
	})
	manager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	if err := manager.Init(); err == nil {
		t.Fatalf("expected init error")
	}
}

func TestTaskManagerPersistsTaskLogInSQLite(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.log.simple",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(_ *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})

	dbPath := filepath.Join(t.TempDir(), "state.db")
	manager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	taskID, err := manager.Spawn("test.log.simple", map[string]any{}, lmfrt.SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn task: %v", err)
	}
	_, timedOut, err := manager.Wait(taskID, 5*time.Second)
	if err != nil {
		t.Fatalf("wait task: %v", err)
	}
	if timedOut {
		t.Fatalf("task wait timed out")
	}

	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	res, err := tc.SqlQuery(
		`SELECT task_id FROM lmfrt__task_log WHERE task_id = ?`,
		taskID,
	)
	if err != nil {
		t.Fatalf("query task log row: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}
	summaries, err := lmfrt.LogState(tc).Summaries(10, false, true)
	if err != nil {
		t.Fatalf("read log summaries: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatalf("expected at least one summary row")
	}
	var found bool
	for _, item := range summaries {
		if item.TaskID != taskID {
			continue
		}
		found = true
		if item.FunctionID != "test.log.simple" {
			t.Fatalf("unexpected function_id: %#v", item.FunctionID)
		}
		if item.Status != lmfrt.TaskStatusDone {
			t.Fatalf("unexpected status: %#v", item.Status)
		}
	}
	if !found {
		t.Fatalf("task summary not found for task_id %s", taskID)
	}
}

func TestTaskManagerInitOnlyOnce(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.__init__",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(_ *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})

	manager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	if err := manager.Init(); err != nil {
		t.Fatalf("first init failed: %v", err)
	}
	if err := manager.Init(); err == nil {
		t.Fatalf("expected second init to fail")
	}
}

func TestRegisterPumpFromInitRuns(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.__init__",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			if _, err := tc.SqlExec(`CREATE TABLE IF NOT EXISTS test__pump_runs(id INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
				return nil, err
			}
			if err := tc.RegisterPump("test.pump", "test.pump.tick", map[string]any{}, 25*time.Millisecond); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	})
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.pump.tick",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			if _, err := tc.SqlExec(`INSERT INTO test__pump_runs DEFAULT VALUES`); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	})
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.pump.count",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			rows, err := tc.SqlQuery(`SELECT COUNT(*) AS c FROM test__pump_runs`)
			if err != nil {
				return nil, err
			}
			return map[string]any{"count": rows.Rows[0]["c"]}, nil
		},
	})

	manager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task manager: %v", err)
	}
	defer func() { _ = manager.Close() }()

	if err := manager.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		res, err := runTaskAndWaitSnapshot(manager, "test.pump.count", map[string]any{}, lmfrt.SpawnOptions{})
		if err != nil {
			t.Fatalf("count pump runs: %v", err)
		}
		if count, ok := res.Output["count"].(int64); ok && count > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pump run")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestTaskManagerResumesTaskIDSequenceFromSQLite(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:          "test.noop",
		WhenToUse:   "test only",
		InputSchema: map[string]any{"type": "object"},
		Exec: func(_ *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	})

	dbPath := filepath.Join(t.TempDir(), "state.db")
	manager1, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("create first task manager: %v", err)
	}

	firstID, err := manager1.Spawn("test.noop", map[string]any{}, lmfrt.SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn first task: %v", err)
	}
	if _, _, err := manager1.Wait(firstID, 5*time.Second); err != nil {
		t.Fatalf("wait first task: %v", err)
	}
	if firstID != "task-000001" {
		t.Fatalf("unexpected first task id: %s", firstID)
	}

	secondID, err := manager1.Spawn("test.noop", map[string]any{}, lmfrt.SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn second task: %v", err)
	}
	if _, _, err := manager1.Wait(secondID, 5*time.Second); err != nil {
		t.Fatalf("wait second task: %v", err)
	}
	if secondID != "task-000002" {
		t.Fatalf("unexpected second task id: %s", secondID)
	}
	if err := manager1.Close(); err != nil {
		t.Fatalf("close first task manager: %v", err)
	}

	manager2, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: dbPath,
	})
	if err != nil {
		t.Fatalf("create second task manager: %v", err)
	}
	defer func() { _ = manager2.Close() }()

	thirdID, err := manager2.Spawn("test.noop", map[string]any{}, lmfrt.SpawnOptions{})
	if err != nil {
		t.Fatalf("spawn third task: %v", err)
	}
	if _, _, err := manager2.Wait(thirdID, 5*time.Second); err != nil {
		t.Fatalf("wait third task: %v", err)
	}
	if thirdID != "task-000003" {
		t.Fatalf("expected resumed task id task-000003, got %s", thirdID)
	}
}

func runTaskAndWaitSnapshot(manager *lmfrt.TaskManager, functionID string, input map[string]any, opts lmfrt.SpawnOptions) (lmfrt.TaskSnapshot, error) {
	taskID, err := manager.Spawn(functionID, input, opts)
	if err != nil {
		return lmfrt.TaskSnapshot{}, err
	}
	snapshot, _, err := manager.Wait(taskID, 0)
	if err != nil {
		return lmfrt.TaskSnapshot{}, err
	}
	if snapshot.Status == lmfrt.TaskStatusFailed {
		if snapshot.Error == "" {
			return lmfrt.TaskSnapshot{}, fmt.Errorf("task %s failed", taskID)
		}
		return lmfrt.TaskSnapshot{}, fmt.Errorf("%s", snapshot.Error)
	}
	if snapshot.Status != lmfrt.TaskStatusDone {
		return lmfrt.TaskSnapshot{}, fmt.Errorf("task %s ended in unexpected status %s", taskID, snapshot.Status)
	}
	return snapshot, nil
}
