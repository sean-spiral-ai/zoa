package lmfrt_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

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

func TestTaskManagerRunUsesSQLitePath(t *testing.T) {
	registry := lmfrt.NewRegistry()
	registry.MustRegister(&lmfrt.Function{
		ID:        "test.sql.counter",
		WhenToUse: "test only",
		Schema:    map[string]any{"type": "object"},
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

	first, err := manager.Run("test.sql.counter", nil)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := manager.Run("test.sql.counter", nil)
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
