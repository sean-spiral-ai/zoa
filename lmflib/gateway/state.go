package gateway

import (
	"context"
	"database/sql"
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

type inboundRow struct {
	ID         int64
	Attempt    int
	Session    string
	Channel    string
	Text       string
	ReceivedAt string
	PumpInput  map[string]any
}

type outboxRow struct {
	ID        int64
	Session   string
	Channel   string
	Text      string
	InReplyTo int64
	SentAt    string
}

const queryConversationEventsBySession = `SELECT id, created_at, message_json
		 FROM gateway__conversation_event
		 WHERE session = ?
		 ORDER BY id`

func newState(tc *lmfrt.TaskContext) *state {
	return &state{tc: tc}
}

func (s *state) init() error {
	now := time.Now().UTC()
	if err := s.initSchema(now); err != nil {
		return err
	}
	if err := s.migrateConversationSnapshot(now); err != nil {
		return err
	}
	return nil
}

func (s *state) initSchema(_ time.Time) error {
	if _, err := s.tc.SqlExec(`CREATE TABLE IF NOT EXISTS gateway__inbound (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session TEXT NOT NULL,
		text TEXT NOT NULL,
		received_at TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'done',
		attempt_count INTEGER NOT NULL DEFAULT 0,
		next_attempt_at TEXT,
		lease_until TEXT,
		processed_at TEXT,
		error_text TEXT
	)`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "session", `TEXT NOT NULL DEFAULT 'default'`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "channel", `TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "status", `TEXT NOT NULL DEFAULT 'done'`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "attempt_count", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "next_attempt_at", `TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "lease_until", `TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "input_json", `TEXT NOT NULL DEFAULT '{}'`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "processed_at", `TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__inbound", "error_text", `TEXT`); err != nil {
		return err
	}
	if _, err := s.tc.SqlExec(`CREATE INDEX IF NOT EXISTS gateway__inbound_session_status_id_idx ON gateway__inbound(session, status, id)`); err != nil {
		return err
	}

	if _, err := s.tc.SqlExec(`CREATE TABLE IF NOT EXISTS gateway__outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session TEXT NOT NULL,
		text TEXT NOT NULL,
		in_reply_to INTEGER,
		sent_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__outbox", "session", `TEXT NOT NULL DEFAULT 'default'`); err != nil {
		return err
	}
	if err := s.ensureColumn("gateway__outbox", "channel", `TEXT`); err != nil {
		return err
	}
	if _, err := s.tc.SqlExec(`CREATE INDEX IF NOT EXISTS gateway__outbox_session_id_idx ON gateway__outbox(session, id)`); err != nil {
		return err
	}

	if _, err := s.tc.SqlExec(`CREATE TABLE IF NOT EXISTS gateway__conversation_event (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session TEXT NOT NULL,
		message_json TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.tc.SqlExec(`CREATE INDEX IF NOT EXISTS gateway__conversation_event_session_id_idx ON gateway__conversation_event(session, id)`); err != nil {
		return err
	}

	return nil
}

func (s *state) migrateConversationSnapshot(now time.Time) error {
	countRes, err := s.tc.SqlQuery(`SELECT COUNT(*) AS c FROM gateway__conversation_event`)
	if err != nil {
		return err
	}
	if len(countRes.Rows) > 0 && int64FromValueDefault(countRes.Rows[0]["c"]) > 0 {
		return nil
	}

	hasLegacyTable, err := s.tableExists("gateway__conversation_state")
	if err != nil {
		return err
	}
	if !hasLegacyTable {
		return nil
	}

	legacy, err := s.tc.SqlQuery(`SELECT history_json FROM gateway__conversation_state WHERE id = 1`)
	if err != nil {
		return err
	}
	if len(legacy.Rows) == 0 {
		return nil
	}
	raw, _ := legacy.Rows[0]["history_json"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var history []baselineagent.ConversationMessage
	if err := json.Unmarshal([]byte(raw), &history); err != nil {
		return fmt.Errorf("decode legacy gateway conversation snapshot: %w", err)
	}
	if len(history) == 0 {
		return nil
	}
	return s.appendConversationMessages(defaultGatewaySession, history, now)
}

func (s *state) tableExists(tableName string) (bool, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT 1 AS ok FROM sqlite_master WHERE type = 'table' AND name = ? LIMIT 1`,
		tableName,
	)
	if err != nil {
		return false, err
	}
	return len(queryRes.Rows) > 0, nil
}

func (s *state) ensureColumn(tableName, columnName, definition string) error {
	hasColumn, err := s.tableHasColumn(tableName, columnName)
	if err != nil {
		return err
	}
	if hasColumn {
		return nil
	}
	_, err = s.tc.SqlExec(
		fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN %s %s`,
			quoteSQLiteIdent(tableName),
			quoteSQLiteIdent(columnName),
			definition,
		),
	)
	return err
}

func (s *state) tableHasColumn(tableName, columnName string) (bool, error) {
	queryRes, err := s.tc.SqlQuery(fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLiteIdent(tableName)))
	if err != nil {
		return false, err
	}
	for _, row := range queryRes.Rows {
		name, _ := row["name"].(string)
		if name == columnName {
			return true, nil
		}
	}
	return false, nil
}

func (s *state) insertInbound(session, channel, text string, pumpInput map[string]any, at time.Time) (int64, error) {
	if pumpInput == nil {
		pumpInput = map[string]any{}
	}
	channel = strings.TrimSpace(channel)
	inputJSON, err := json.Marshal(pumpInput)
	if err != nil {
		return 0, fmt.Errorf("encode inbound pump input: %w", err)
	}
	res, err := s.tc.SqlExec(
		`INSERT INTO gateway__inbound(session, channel, text, input_json, received_at, status, attempt_count, next_attempt_at) VALUES (?, ?, ?, ?, ?, 'pending', 0, ?)`,
		session, channel, text, string(inputJSON), at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, err
	}
	if res.LastInsertID == nil {
		return 0, nil
	}
	return *res.LastInsertID, nil
}

func (s *state) claimDueInbound(session string, now time.Time, leaseDuration time.Duration) (*inboundRow, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		inboundID, err := s.claimDueInboundID(session, now, leaseDuration)
		if err != nil {
			return nil, err
		}
		if inboundID == 0 {
			return nil, nil
		}
		row, err := s.inboundByID(inboundID)
		if err != nil {
			return nil, err
		}
		if row != nil {
			return row, nil
		}
	}
	return nil, nil
}

func (s *state) claimDueInboundID(session string, now time.Time, leaseDuration time.Duration) (int64, error) {
	if leaseDuration <= 0 {
		leaseDuration = time.Minute
	}
	nowText := now.UTC().Format(time.RFC3339Nano)
	leaseUntilText := now.UTC().Add(leaseDuration).Format(time.RFC3339Nano)

	var inboundID int64
	err := s.tc.SqlTx(func(tx *sql.Tx) error {
		row := tx.QueryRowContext(
			s.tc.Context(),
			`SELECT id
			 FROM gateway__inbound
			 WHERE session = ?
			   AND (
			       (status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
			       OR
			       (status = 'processing' AND (lease_until IS NULL OR lease_until <= ?))
			   )
			 ORDER BY id
			 LIMIT 1`,
			session,
			nowText,
			nowText,
		)
		if err := row.Scan(&inboundID); err != nil {
			if err == sql.ErrNoRows {
				inboundID = 0
				return nil
			}
			return err
		}
		res, err := tx.ExecContext(
			s.tc.Context(),
			`UPDATE gateway__inbound
			 SET status = 'processing',
			     attempt_count = COALESCE(attempt_count, 0) + 1,
			     lease_until = ?,
			     processed_at = NULL
			 WHERE id = ?
			   AND (
			       (status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
			       OR
			       (status = 'processing' AND (lease_until IS NULL OR lease_until <= ?))
			   )`,
			leaseUntilText,
			inboundID,
			nowText,
			nowText,
		)
		if err != nil {
			return err
		}
		if rowsAffected, err := res.RowsAffected(); err == nil && rowsAffected == 0 {
			inboundID = 0
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inboundID, nil
}

func (s *state) inboundByID(inboundID int64) (*inboundRow, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT id, COALESCE(attempt_count, 0) AS attempt_count, session, channel, text, input_json, received_at
		 FROM gateway__inbound
		 WHERE id = ?
		 LIMIT 1`,
		inboundID,
	)
	if err != nil {
		return nil, err
	}
	if len(queryRes.Rows) == 0 {
		return nil, nil
	}
	item := queryRes.Rows[0]
	id, ok := lmflib.Int64FromValue(item["id"])
	if !ok {
		return nil, fmt.Errorf("decode inbound id")
	}
	attempt := int(int64FromValueDefault(item["attempt_count"]))
	if attempt < 1 {
		attempt = 1
	}
	session, _ := item["session"].(string)
	channel, _ := item["channel"].(string)
	text, _ := item["text"].(string)
	inputJSON, _ := item["input_json"].(string)
	receivedAt, _ := item["received_at"].(string)
	pumpInput := map[string]any{}
	inputJSON = strings.TrimSpace(inputJSON)
	if inputJSON != "" {
		if err := json.Unmarshal([]byte(inputJSON), &pumpInput); err != nil {
			return nil, fmt.Errorf("decode inbound pump input: %w", err)
		}
	}
	return &inboundRow{
		ID:         id,
		Attempt:    attempt,
		Session:    session,
		Channel:    channel,
		Text:       text,
		ReceivedAt: receivedAt,
		PumpInput:  pumpInput,
	}, nil
}

func (s *state) markInboundDone(inboundID int64, at time.Time) error {
	_, err := s.tc.SqlExec(
		`UPDATE gateway__inbound
		 SET status = 'done', processed_at = ?, error_text = NULL, lease_until = NULL
		 WHERE id = ?`,
		at.UTC().Format(time.RFC3339Nano),
		inboundID,
	)
	return err
}

func (s *state) markInboundRetry(inboundID int64, errorText string, nextAttemptAt time.Time) error {
	_, err := s.tc.SqlExec(
		`UPDATE gateway__inbound
		 SET status = 'pending',
		     next_attempt_at = ?,
		     lease_until = NULL,
		     error_text = ?
		 WHERE id = ?`,
		nextAttemptAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(errorText),
		inboundID,
	)
	return err
}

func (s *state) markInboundFailed(inboundID int64, errorText string, at time.Time) error {
	_, err := s.tc.SqlExec(
		`UPDATE gateway__inbound
		 SET status = 'failed',
		     processed_at = ?,
		     lease_until = NULL,
		     error_text = ?
		 WHERE id = ?`,
		at.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(errorText),
		inboundID,
	)
	return err
}

func (s *state) insertOutbox(session, channel, text string, inReplyTo *int64, at time.Time) (int64, error) {
	var (
		res lmfrt.SqlExecResult
		err error
	)
	channel = strings.TrimSpace(channel)
	if inReplyTo == nil {
		res, err = s.tc.SqlExec(
			`INSERT INTO gateway__outbox(session, channel, text, in_reply_to, sent_at) VALUES (?, ?, ?, NULL, ?)`,
			session, channel, text, at.UTC().Format(time.RFC3339Nano),
		)
	} else {
		res, err = s.tc.SqlExec(
			`INSERT INTO gateway__outbox(session, channel, text, in_reply_to, sent_at) VALUES (?, ?, ?, ?, ?)`,
			session, channel, text, *inReplyTo, at.UTC().Format(time.RFC3339Nano),
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

func (s *state) outboxSince(session string, lastID int64, limit int) ([]outboxRow, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT id, session, channel, text, COALESCE(in_reply_to, 0) AS in_reply_to, sent_at
		 FROM gateway__outbox
		 WHERE id > ? AND session = ?
		 ORDER BY id
		 LIMIT ?`,
		lastID, session, limit,
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
		sessionVal, _ := item["session"].(string)
		channelVal, _ := item["channel"].(string)
		textVal, _ := item["text"].(string)
		sentAtVal, _ := item["sent_at"].(string)
		rows = append(rows, outboxRow{
			ID:        id,
			Session:   sessionVal,
			Channel:   channelVal,
			Text:      textVal,
			InReplyTo: inReplyTo,
			SentAt:    sentAtVal,
		})
	}
	return rows, nil
}

func (s *state) recentOutbox(session string, limit int) ([]outboxRow, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT id, session, channel, text, COALESCE(in_reply_to, 0) AS in_reply_to, sent_at
		 FROM gateway__outbox
		 WHERE session = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		session, limit,
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
		sessionVal, _ := item["session"].(string)
		channelVal, _ := item["channel"].(string)
		textVal, _ := item["text"].(string)
		sentAtVal, _ := item["sent_at"].(string)
		rows = append(rows, outboxRow{
			ID:        id,
			Session:   sessionVal,
			Channel:   channelVal,
			Text:      textVal,
			InReplyTo: inReplyTo,
			SentAt:    sentAtVal,
		})
	}
	return rows, nil
}

func (s *state) outboxMaxID(session string) (int64, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT COALESCE(MAX(id), 0) AS max_id FROM gateway__outbox WHERE session = ?`,
		session,
	)
	if err != nil {
		return 0, err
	}
	if len(queryRes.Rows) == 0 {
		return 0, nil
	}
	return int64FromValueDefault(queryRes.Rows[0]["max_id"]), nil
}

func (s *state) outboxCount(session string) (int64, error) {
	queryRes, err := s.tc.SqlQuery(`SELECT COUNT(*) AS c FROM gateway__outbox WHERE session = ?`, session)
	if err != nil {
		return 0, err
	}
	if len(queryRes.Rows) == 0 {
		return 0, nil
	}
	return int64FromValueDefault(queryRes.Rows[0]["c"]), nil
}

func (s *state) pendingCount(session string) (int64, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT COUNT(*) AS c
		 FROM gateway__inbound
		 WHERE session = ? AND status = 'pending'`,
		session,
	)
	if err != nil {
		return 0, err
	}
	if len(queryRes.Rows) == 0 {
		return 0, nil
	}
	return int64FromValueDefault(queryRes.Rows[0]["c"]), nil
}

func (s *state) processingCount(session string) (int64, error) {
	queryRes, err := s.tc.SqlQuery(
		`SELECT COUNT(*) AS c
		 FROM gateway__inbound
		 WHERE session = ? AND status = 'processing'`,
		session,
	)
	if err != nil {
		return 0, err
	}
	if len(queryRes.Rows) == 0 {
		return 0, nil
	}
	return int64FromValueDefault(queryRes.Rows[0]["c"]), nil
}

func (s *state) loadConversationHistory(session string) ([]baselineagent.ConversationMessage, error) {
	queryRes, err := s.tc.SqlQuery(queryConversationEventsBySession, session)
	if err != nil {
		return nil, err
	}
	history := make([]baselineagent.ConversationMessage, 0, len(queryRes.Rows))
	for _, item := range queryRes.Rows {
		raw, _ := item["message_json"].(string)
		msg, ok, err := decodeConversationMessage(raw)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		history = append(history, msg)
	}
	return history, nil
}

func decodeConversationMessage(raw string) (baselineagent.ConversationMessage, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return baselineagent.ConversationMessage{}, false, nil
	}
	var msg baselineagent.ConversationMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return baselineagent.ConversationMessage{}, false, fmt.Errorf("decode gateway conversation event: %w", err)
	}
	return msg, true, nil
}

func (s *state) appendConversationMessages(session string, messages []baselineagent.ConversationMessage, at time.Time) error {
	if len(messages) == 0 {
		return nil
	}
	return s.tc.SqlTx(func(tx *sql.Tx) error {
		ctx := context.Background()
		if s.tc != nil {
			ctx = s.tc.Context()
		}
		createdAt := at.UTC().Format(time.RFC3339Nano)
		for _, msg := range messages {
			data, err := json.Marshal(msg)
			if err != nil {
				return fmt.Errorf("encode conversation message: %w", err)
			}
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO gateway__conversation_event(session, message_json, created_at) VALUES (?, ?, ?)`,
				session,
				string(data),
				createdAt,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func int64FromValueDefault(v any) int64 {
	if out, ok := lmflib.Int64FromValue(v); ok {
		return out
	}
	return 0
}

func quoteSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
