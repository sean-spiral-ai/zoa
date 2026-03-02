package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	baselineagent "zoa/baselineagent"
	"zoa/lmflib"
	lmfrt "zoa/lmfrt"
)

// state centralizes gateway SQLite state reads/writes.
// Convention: all gateway mutable state access should go through this type.
// The namespace initializer `gateway.__init__` should call `state.init()`.
type state struct {
	tc *lmfrt.TaskContext
}

type outboxRow struct {
	ID        int64
	Channel   string
	Text      string
	InReplyTo int64
	SentAt    string
}

func newState(tc *lmfrt.TaskContext) *state {
	return &state{tc: tc}
}

func (s *state) init() error {
	now := time.Now().UTC()
	return s.initSchema(now)
}

func (s *state) initSchema(now time.Time) error {
	if _, err := s.tc.SqlExec(`CREATE TABLE IF NOT EXISTS gateway__inbound (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel TEXT NOT NULL,
		text TEXT NOT NULL,
		received_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.tc.SqlExec(`CREATE TABLE IF NOT EXISTS gateway__outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel TEXT NOT NULL,
		text TEXT NOT NULL,
		in_reply_to INTEGER,
		sent_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.tc.SqlExec(`CREATE INDEX IF NOT EXISTS gateway__outbox_channel_id_idx ON gateway__outbox(channel, id)`); err != nil {
		return err
	}
	if _, err := s.tc.SqlExec(`CREATE TABLE IF NOT EXISTS gateway__conversation_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		history_json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.tc.SqlExec(
		`INSERT OR IGNORE INTO gateway__conversation_state(id, history_json, updated_at) VALUES (1, '[]', ?)`,
		now.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	return nil
}

func (s *state) insertInbound(channel, text string, at time.Time) (int64, error) {
	res, err := s.tc.SqlExec(
		`INSERT INTO gateway__inbound(channel, text, received_at) VALUES (?, ?, ?)`,
		channel, text, at.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, err
	}
	if res.LastInsertID == nil {
		return 0, nil
	}
	return *res.LastInsertID, nil
}

func (s *state) insertOutbox(channel, text string, inReplyTo *int64, at time.Time) (int64, error) {
	var (
		res lmfrt.SqlExecResult
		err error
	)
	if inReplyTo == nil {
		res, err = s.tc.SqlExec(
			`INSERT INTO gateway__outbox(channel, text, in_reply_to, sent_at) VALUES (?, ?, NULL, ?)`,
			channel, text, at.UTC().Format(time.RFC3339Nano),
		)
	} else {
		res, err = s.tc.SqlExec(
			`INSERT INTO gateway__outbox(channel, text, in_reply_to, sent_at) VALUES (?, ?, ?, ?)`,
			channel, text, *inReplyTo, at.UTC().Format(time.RFC3339Nano),
		)
	}
	if err != nil {
		return 0, err
	}
	if res.LastInsertID == nil {
		return 0, nil
	}
	return *res.LastInsertID, nil
}

func (s *state) outboxSince(channel string, lastID int64, limit int) ([]outboxRow, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT id, channel, text, COALESCE(in_reply_to, 0) AS in_reply_to, sent_at
		 FROM gateway__outbox
		 WHERE id > ? AND channel = ?
		 ORDER BY id
		 LIMIT ?`,
		lastID, channel, limit,
	)
	if err != nil {
		return nil, err
	}
	rows := make([]outboxRow, 0, len(queryRes.Rows))
	for _, item := range queryRes.Rows {
		id, ok := lmflib.Int64FromValue(item["id"])
		if !ok {
			continue
		}
		inReplyTo, _ := lmflib.Int64FromValue(item["in_reply_to"])
		channelVal, _ := item["channel"].(string)
		textVal, _ := item["text"].(string)
		sentAtVal, _ := item["sent_at"].(string)
		rows = append(rows, outboxRow{
			ID:        id,
			Channel:   channelVal,
			Text:      textVal,
			InReplyTo: inReplyTo,
			SentAt:    sentAtVal,
		})
	}
	return rows, nil
}

func (s *state) recentOutbox(limit int) ([]outboxRow, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT id, channel, text, COALESCE(in_reply_to, 0) AS in_reply_to, sent_at
		 FROM gateway__outbox
		 ORDER BY id DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	rows := make([]outboxRow, 0, len(queryRes.Rows))
	for _, item := range queryRes.Rows {
		id, ok := lmflib.Int64FromValue(item["id"])
		if !ok {
			continue
		}
		inReplyTo, _ := lmflib.Int64FromValue(item["in_reply_to"])
		channelVal, _ := item["channel"].(string)
		textVal, _ := item["text"].(string)
		sentAtVal, _ := item["sent_at"].(string)
		rows = append(rows, outboxRow{
			ID:        id,
			Channel:   channelVal,
			Text:      textVal,
			InReplyTo: inReplyTo,
			SentAt:    sentAtVal,
		})
	}
	return rows, nil
}

func (s *state) outboxCount() (int64, error) {
	queryRes, err := s.tc.SqlQuery(`SELECT COUNT(*) AS c FROM gateway__outbox`)
	if err != nil {
		return 0, err
	}
	if len(queryRes.Rows) == 0 {
		return 0, nil
	}
	return int64FromValueDefault(queryRes.Rows[0]["c"]), nil
}

func (s *state) loadConversationHistory() ([]baselineagent.ConversationMessage, error) {
	queryRes, err := s.tc.SqlQuery(`SELECT history_json FROM gateway__conversation_state WHERE id = 1`)
	if err != nil {
		return nil, err
	}
	if len(queryRes.Rows) == 0 {
		return []baselineagent.ConversationMessage{}, nil
	}
	raw, _ := queryRes.Rows[0]["history_json"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []baselineagent.ConversationMessage{}, nil
	}
	var history []baselineagent.ConversationMessage
	if err := json.Unmarshal([]byte(raw), &history); err != nil {
		return nil, fmt.Errorf("decode gateway conversation history: %w", err)
	}
	if history == nil {
		return []baselineagent.ConversationMessage{}, nil
	}
	return history, nil
}

func (s *state) saveConversationHistory(history []baselineagent.ConversationMessage, at time.Time) error {
	if history == nil {
		history = []baselineagent.ConversationMessage{}
	}
	data, err := json.Marshal(history)
	if err != nil {
		return fmt.Errorf("encode gateway conversation history: %w", err)
	}
	_, err = s.tc.SqlExec(
		`UPDATE gateway__conversation_state SET history_json = ?, updated_at = ? WHERE id = 1`,
		string(data),
		at.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func int64FromValueDefault(v any) int64 {
	if out, ok := lmflib.Int64FromValue(v); ok {
		return out
	}
	return 0
}
