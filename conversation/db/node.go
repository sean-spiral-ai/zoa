package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (db *DB) Append(parentHash string, msg Message) (string, error) {
	if parentHash == "" {
		parentHash = RootHash
	}
	if err := db.ensureNodeExists(parentHash); err != nil {
		return "", err
	}
	return db.insertNode(db.sql, parentHash, msg)
}

func (db *DB) LoadChain(hash string) ([]Node, error) {
	if hash == "" {
		hash = RootHash
	}
	rows, err := db.sql.Query(
		`WITH RECURSIVE chain(hash, parent_hash, role, message_json, created_at, depth) AS (
			SELECT hash, parent_hash, role, message_json, created_at, 0
			FROM conversation_node
			WHERE hash = ?
			UNION ALL
			SELECT n.hash, n.parent_hash, n.role, n.message_json, n.created_at, chain.depth + 1
			FROM conversation_node n
			JOIN chain ON n.hash = chain.parent_hash
			WHERE chain.parent_hash <> ''
		)
		SELECT hash, parent_hash, role, message_json, created_at
		FROM chain
		ORDER BY depth DESC`,
		hash,
	)
	if err != nil {
		return nil, fmt.Errorf("load chain: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var (
			node       Node
			role       string
			messageRaw string
			createdRaw string
		)
		if err := rows.Scan(&node.Hash, &node.ParentHash, &role, &messageRaw, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan chain: %w", err)
		}
		if role == "root" {
			continue
		}
		if err := json.Unmarshal([]byte(messageRaw), &node.Message); err != nil {
			return nil, fmt.Errorf("decode message %s: %w", node.Hash, err)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, fmt.Errorf("parse created_at %s: %w", node.Hash, err)
		}
		node.CreatedAt = createdAt
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chain: %w", err)
	}
	if len(nodes) == 0 && hash != RootHash {
		if err := db.ensureNodeExists(hash); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

func (db *DB) ensureNodeExists(hash string) error {
	var found string
	err := db.sql.QueryRow(`SELECT hash FROM conversation_node WHERE hash = ?`, hash).Scan(&found)
	if err == sql.ErrNoRows {
		return ErrNodeNotFound
	}
	if err != nil {
		return fmt.Errorf("load node %s: %w", hash, err)
	}
	return nil
}

func (db *DB) insertNode(exec interface {
	Exec(query string, args ...any) (sql.Result, error)
}, parentHash string, msg Message) (string, error) {
	msgJSON, err := CanonicalJSON(msg)
	if err != nil {
		return "", fmt.Errorf("encode message: %w", err)
	}
	hash := NodeHash(parentHash, msgJSON)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := exec.Exec(
		`INSERT OR IGNORE INTO conversation_node (hash, parent_hash, role, message_json, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		hash, parentHash, msg.Role, string(msgJSON), now,
	); err != nil {
		return "", fmt.Errorf("insert node: %w", err)
	}
	return hash, nil
}
