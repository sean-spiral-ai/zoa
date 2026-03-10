package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	convdb "zoa/conversation/db"
	"zoa/internal/agentloop/llm"

	_ "modernc.org/sqlite"
)

func TestDebugGetConversation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE conversation_node (
		hash TEXT PRIMARY KEY,
		parent_hash TEXT,
		role TEXT NOT NULL,
		message_json TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE conversation_ref (
		name TEXT PRIMARY KEY,
		hash TEXT NOT NULL,
		leased_by TEXT NOT NULL DEFAULT '',
		lease_until TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create refs table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO conversation_node(hash, parent_hash, role, message_json, created_at) VALUES (?, '', 'root', '{}', '2026-03-04T00:00:00Z')`, convdb.RootHash)
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}

	msg := llm.Message{
		Role: llm.RoleUser,
		Text: "hello",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}

	hash := convdb.NodeHash(convdb.RootHash, data)
	_, err = db.Exec(`INSERT INTO conversation_node(hash, parent_hash, role, message_json, created_at) VALUES (?, ?, 'user', ?, '2026-03-04T00:00:00Z')`, hash, convdb.RootHash, string(data))
	if err != nil {
		t.Fatalf("insert node: %v", err)
	}
	_, err = db.Exec(`INSERT INTO conversation_ref(name, hash, updated_at) VALUES ('sessions/default', ?, '2026-03-04T00:00:00Z')`, hash)
	if err != nil {
		t.Fatalf("insert ref: %v", err)
	}

	events, err := DebugGetConversation(context.Background(), db, "")
	if err != nil {
		t.Fatalf("DebugGetConversation: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event for default session, got %d", len(events))
	}
	if got := string(events[0].Message.Role); got != string(llm.RoleUser) {
		t.Fatalf("unexpected role: %q", got)
	}
	if got := events[0].Message.Text; got != "hello" {
		t.Fatalf("unexpected text: %q", got)
	}
}
