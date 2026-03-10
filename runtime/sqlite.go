package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func ConversationSQLitePath(stateSQLitePath string) string {
	path := strings.TrimSpace(stateSQLitePath)
	if path == "" || path == ":memory:" {
		return path
	}
	return filepath.Join(filepath.Dir(path), "conversation.db")
}

func UserSQLitePath(runtimeSQLitePath string) string {
	path := strings.TrimSpace(runtimeSQLitePath)
	if path == "" || path == ":memory:" {
		return path
	}
	return filepath.Join(filepath.Dir(path), "state.db")
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	Close() error
}

func openSQLite(sqlitePath string) (*sql.DB, string, error) {
	path := strings.TrimSpace(sqlitePath)
	if path == "" {
		return nil, "", nil
	}
	resolved := path
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, "", fmt.Errorf("resolve sqlite path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, "", fmt.Errorf("create sqlite directory: %w", err)
		}
		resolved = abs
	}

	db, err := sql.Open("sqlite", resolved)
	if err != nil {
		return nil, "", fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("set sqlite WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("set sqlite busy_timeout: %w", err)
	}
	return db, resolved, nil
}
