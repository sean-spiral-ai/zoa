package llmtrace

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Node represents a single message node in the content-addressable tree.
type Node struct {
	ID           int64  `json:"id"`
	Hash         string `json:"hash"`
	ParentHash   string `json:"parent_hash"`
	Role         string `json:"role"`
	Summary      string `json:"summary"`
	MessageJSON  string `json:"message_json"`
	MetadataJSON string `json:"metadata_json"`
	CreatedAt    string `json:"created_at"`
}

// Store is a SQLite-backed content-addressable node store.
type Store struct {
	db *sql.DB
}

const createTableSQL = `
CREATE TABLE IF NOT EXISTS llmtrace_node (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	hash          TEXT UNIQUE NOT NULL,
	parent_hash   TEXT,
	role          TEXT NOT NULL,
	summary       TEXT NOT NULL DEFAULT '',
	message_json  TEXT NOT NULL DEFAULT '{}',
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_llmtrace_node_parent ON llmtrace_node(parent_hash);
`

// NewStore opens or creates an llmtrace SQLite database at the given path.
func NewStore(dbPath string) (*Store, error) {
	path := strings.TrimSpace(dbPath)
	if path == "" {
		return nil, fmt.Errorf("llmtrace db path is required")
	}
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve llmtrace db path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("create llmtrace db directory: %w", err)
		}
		path = abs
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open llmtrace db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping llmtrace db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec(createTableSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create llmtrace tables: %w", err)
	}

	s := &Store{db: db}
	// Insert root sentinel
	if err := s.insertRoot(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) insertRoot() error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO llmtrace_node (hash, parent_hash, role, summary, message_json, metadata_json, created_at)
		 VALUES (?, NULL, 'root', 'ROOT', '{}', '{}', ?)`,
		RootHash, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// Insert adds a node to the store. Returns the hash. Idempotent (INSERT OR IGNORE).
func (s *Store) Insert(parentHash, role, summary, messageJSON, metadataJSON string) (string, error) {
	hash := NodeHash(parentHash, []byte(messageJSON))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO llmtrace_node (hash, parent_hash, role, summary, message_json, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hash, parentHash, role, summary, messageJSON, metadataJSON, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert llmtrace node: %w", err)
	}
	return hash, nil
}

// Children returns all child nodes of the given parent hash.
func (s *Store) Children(parentHash string) ([]Node, error) {
	rows, err := s.db.Query(
		`SELECT id, hash, parent_hash, role, summary, message_json, metadata_json, created_at
		 FROM llmtrace_node WHERE parent_hash = ? ORDER BY id`,
		parentHash,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// Since returns nodes inserted after the given auto-increment ID.
func (s *Store) Since(lastID int64) ([]Node, error) {
	rows, err := s.db.Query(
		`SELECT id, hash, parent_hash, role, summary, message_json, metadata_json, created_at
		 FROM llmtrace_node WHERE id > ? ORDER BY id`,
		lastID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// AllNodes returns every node in insertion order.
func (s *Store) AllNodes() ([]Node, error) {
	rows, err := s.db.Query(
		`SELECT id, hash, parent_hash, role, summary, message_json, metadata_json, created_at
		 FROM llmtrace_node ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func scanNodes(rows *sql.Rows) ([]Node, error) {
	var nodes []Node
	for rows.Next() {
		var n Node
		var parentHash sql.NullString
		if err := rows.Scan(&n.ID, &n.Hash, &parentHash, &n.Role, &n.Summary, &n.MessageJSON, &n.MetadataJSON, &n.CreatedAt); err != nil {
			return nil, err
		}
		if parentHash.Valid {
			n.ParentHash = parentHash.String
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}
