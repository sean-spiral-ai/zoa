package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"zoa/internal/agentloop/llm"
)

type DB struct {
	sql *sql.DB
}

type Node struct {
	Hash       string
	ParentHash string
	Message    Message
	CreatedAt  time.Time
}

type TraceNode struct {
	ID           int64  `json:"id"`
	Hash         string `json:"hash"`
	ParentHash   string `json:"parent_hash"`
	Role         string `json:"role"`
	Summary      string `json:"summary"`
	MessageJSON  string `json:"message_json"`
	MetadataJSON string `json:"metadata_json"`
	CreatedAt    string `json:"created_at"`
}

type Message = llm.Message

type RefSnapshot struct {
	Name       string
	Hash       string
	LeasedBy   string
	LeaseUntil time.Time
}

const defaultLeaseDuration = 30 * time.Second

type LeasedRef struct {
	db       *DB
	name     string
	runnerID string

	mu         sync.Mutex
	hash       string
	leaseUntil time.Time
	released   bool
}

func Open(dbPath string) (*DB, error) {
	path := strings.TrimSpace(dbPath)
	if path == "" {
		return nil, fmt.Errorf("conversation db path is required")
	}
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve conversation db path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("create conversation db directory: %w", err)
		}
		path = abs
	}

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open conversation db: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping conversation db: %w", err)
	}
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := sqlDB.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := sqlDB.Exec(schemaSQL); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("initialize conversation schema: %w", err)
	}
	db := &DB{sql: sqlDB}
	if err := db.insertRoot(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

func (db *DB) insertRoot() error {
	_, err := db.sql.Exec(
		`INSERT OR IGNORE INTO conversation_node (hash, parent_hash, role, message_json, created_at)
		 VALUES (?, '', 'root', '{}', ?)`,
		RootHash, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}
