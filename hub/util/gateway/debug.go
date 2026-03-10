package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"zoa/llm"
)

type DebugConversationEvent struct {
	ID        int64
	CreatedAt string
	Message   llm.Message
}

func DebugGetConversation(ctx context.Context, db *sql.DB, session string) ([]DebugConversationEvent, error) {
	if db == nil {
		return nil, fmt.Errorf("db is nil")
	}
	session = normalizeSession(session)
	if session == "" {
		session = defaultGatewaySession
	}
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE chain(hash, parent_hash, role, message_json, created_at, depth) AS (
			SELECT n.hash, n.parent_hash, n.role, n.message_json, n.created_at, 0
			FROM conversation_ref r
			JOIN conversation_node n ON n.hash = r.hash
			WHERE r.name = ?
			UNION ALL
			SELECT n.hash, n.parent_hash, n.role, n.message_json, n.created_at, chain.depth + 1
			FROM conversation_node n
			JOIN chain ON n.hash = chain.parent_hash
			WHERE chain.parent_hash <> ''
		)
		SELECT hash, role, message_json, created_at
		FROM chain
		WHERE role <> 'root'
		ORDER BY depth DESC
	`, "sessions/"+session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []DebugConversationEvent{}
	var nextID int64 = 1
	for rows.Next() {
		var (
			hash      string
			role      string
			createdAt string
			raw       string
		)
		if err := rows.Scan(&hash, &role, &raw, &createdAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return nil, err
		}
		events = append(events, DebugConversationEvent{
			ID:        nextID,
			CreatedAt: createdAt,
			Message:   msg,
		})
		nextID++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
