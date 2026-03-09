package gateway

import (
	"context"
	"database/sql"
	"fmt"

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

	rows, err := db.QueryContext(ctx, queryConversationEventsBySession, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []DebugConversationEvent{}
	for rows.Next() {
		var (
			id        int64
			createdAt string
			raw       string
		)
		if err := rows.Scan(&id, &createdAt, &raw); err != nil {
			return nil, err
		}
		msg, ok, err := decodeConversationMessage(raw)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		events = append(events, DebugConversationEvent{
			ID:        id,
			CreatedAt: createdAt,
			Message:   msg,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
