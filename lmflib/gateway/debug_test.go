package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"zoa/llm"

	_ "modernc.org/sqlite"
)

func TestDebugGetConversation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE gateway__conversation_event (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session TEXT NOT NULL,
		message_json TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	msg := llm.Message{
		Role: llm.RoleUser,
		Text: "hello",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}

	_, err = db.Exec(`INSERT INTO gateway__conversation_event(session, message_json, created_at) VALUES ('default', ?, '2026-03-04T00:00:00Z')`, string(data))
	if err != nil {
		t.Fatalf("insert default session row: %v", err)
	}
	_, err = db.Exec(`INSERT INTO gateway__conversation_event(session, message_json, created_at) VALUES ('other', ?, '2026-03-04T00:00:01Z')`, string(data))
	if err != nil {
		t.Fatalf("insert other session row: %v", err)
	}
	_, err = db.Exec(`INSERT INTO gateway__conversation_event(session, message_json, created_at) VALUES ('default', '   ', '2026-03-04T00:00:02Z')`)
	if err != nil {
		t.Fatalf("insert empty message row: %v", err)
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
