package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	baselineagent "zoa/baselineagent"
)

type TaskLogSummary struct {
	TaskID        string
	FunctionID    string
	Status        TaskStatus
	Error         string
	UpdatedAt     string
	HideByDefault bool
}

type TaskLogState struct {
	tc *TaskContext
}

func LogState(tc *TaskContext) *TaskLogState {
	return &TaskLogState{tc: tc}
}

func (s *TaskLogState) Init() error {
	if err := s.ensureInitialized(); err != nil {
		return err
	}
	// Migrate: rename old lmfrt__task_log table if it exists and new one doesn't yet.
	if err := s.migrateFromLmfrt(); err != nil {
		return fmt.Errorf("migrate lmfrt__task_log: %w", err)
	}
	if err := s.exec(`CREATE TABLE IF NOT EXISTS runtime__task_log (
		task_id TEXT PRIMARY KEY,
		function_id TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		output_json TEXT,
		error_text TEXT,
		hide_by_default INTEGER NOT NULL DEFAULT 0,
		input_json TEXT,
		conversation_json TEXT,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create runtime__task_log table: %w", err)
	}
	if err := s.ensureColumn("runtime__task_log", "hide_by_default", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("ensure runtime__task_log.hide_by_default: %w", err)
	}
	if err := s.exec(`CREATE INDEX IF NOT EXISTS runtime__task_log_status_updated_idx ON runtime__task_log(status, updated_at DESC)`); err != nil {
		return fmt.Errorf("create runtime__task_log index: %w", err)
	}
	return nil
}

func (s *TaskLogState) migrateFromLmfrt() error {
	// Check if the old table exists.
	rows, err := s.query(`SELECT name FROM sqlite_master WHERE type='table' AND name='lmfrt__task_log'`)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil // Nothing to migrate.
	}
	// Check if the new table already exists (avoid clobbering).
	rows, err = s.query(`SELECT name FROM sqlite_master WHERE type='table' AND name='runtime__task_log'`)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return nil // New table already exists; skip migration.
	}
	// Rename table and index.
	if err := s.exec(`ALTER TABLE lmfrt__task_log RENAME TO runtime__task_log`); err != nil {
		return fmt.Errorf("rename table: %w", err)
	}
	// Drop old index (ALTER INDEX RENAME not supported in SQLite) — the new one
	// will be created by Init() immediately after this migration returns.
	s.exec(`DROP INDEX IF EXISTS lmfrt__task_log_status_updated_idx`)
	return nil
}

func (s *TaskLogState) Summaries(limit int, onlyRunning bool, includeHidden bool) ([]TaskLogSummary, error) {
	if err := s.ensureInitialized(); err != nil {
		return nil, err
	}

	query := `SELECT task_id, function_id, status, error_text, updated_at, hide_by_default
		FROM runtime__task_log`
	args := []any{}
	clauses := []string{}
	if onlyRunning {
		clauses = append(clauses, `status = ?`)
		args = append(args, string(TaskStatusRunning))
	}
	if !includeHidden {
		clauses = append(clauses, `COALESCE(hide_by_default, 0) = 0`)
	}
	if len(clauses) > 0 {
		query += ` WHERE ` + strings.Join(clauses, ` AND `)
	}
	query += ` ORDER BY updated_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.query(query, args...)
	if err != nil {
		return nil, err
	}
	items := make([]TaskLogSummary, 0, len(rows))
	for _, row := range rows {
		taskID, _ := row["task_id"].(string)
		functionID, _ := row["function_id"].(string)
		statusText, _ := row["status"].(string)
		errorText, _ := row["error_text"].(string)
		updatedAt, _ := row["updated_at"].(string)
		hideByDefault := int64FromValueDefault(row["hide_by_default"]) != 0
		items = append(items, TaskLogSummary{
			TaskID:        taskID,
			FunctionID:    functionID,
			Status:        TaskStatus(strings.TrimSpace(statusText)),
			Error:         errorText,
			UpdatedAt:     updatedAt,
			HideByDefault: hideByDefault,
		})
	}
	return items, nil
}

func (s *TaskLogState) Get(taskID string) (TaskLogRecord, error) {
	if err := s.ensureInitialized(); err != nil {
		return TaskLogRecord{}, err
	}
	rows, err := s.query(
		`SELECT task_id, function_id, status, created_at, started_at, finished_at,
			output_json, error_text, hide_by_default, input_json, conversation_json, updated_at
		FROM runtime__task_log
		WHERE task_id = ?`,
		taskID,
	)
	if err != nil {
		return TaskLogRecord{}, err
	}
	if len(rows) == 0 {
		return TaskLogRecord{}, fmt.Errorf("unknown task_id: %s", taskID)
	}
	return taskLogRecordFromRow(rows[0])
}

func (s *TaskLogState) upsert(record TaskLogRecord) error {
	if err := s.ensureInitialized(); err != nil {
		return err
	}

	inputJSON, err := json.Marshal(record.Input)
	if err != nil {
		return fmt.Errorf("encode task input: %w", err)
	}
	outputJSON, err := json.Marshal(record.Output)
	if err != nil {
		return fmt.Errorf("encode task output: %w", err)
	}
	conversationJSON, err := json.Marshal(record.Conversation)
	if err != nil {
		return fmt.Errorf("encode task conversation: %w", err)
	}
	var startedAt any
	if record.StartedAt != nil {
		startedAt = record.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	var finishedAt any
	if record.FinishedAt != nil {
		finishedAt = record.FinishedAt.UTC().Format(time.RFC3339Nano)
	}

	if err := s.exec(
		`INSERT INTO runtime__task_log(
			task_id, function_id, status, created_at, started_at, finished_at,
			output_json, error_text, hide_by_default, input_json, conversation_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			function_id = excluded.function_id,
			status = excluded.status,
			created_at = excluded.created_at,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at,
			output_json = excluded.output_json,
			error_text = excluded.error_text,
			hide_by_default = excluded.hide_by_default,
			input_json = excluded.input_json,
			conversation_json = excluded.conversation_json,
			updated_at = excluded.updated_at`,
		record.TaskID,
		record.FunctionID,
		string(record.Status),
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		startedAt,
		finishedAt,
		string(outputJSON),
		record.Error,
		boolToSQLiteInt(record.HideByDefault),
		string(inputJSON),
		string(conversationJSON),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("upsert runtime task log: %w", err)
	}
	return nil
}

func (s *TaskLogState) MaxTaskSequence() (uint64, error) {
	if err := s.ensureInitialized(); err != nil {
		return 0, err
	}
	rows, err := s.query(
		`SELECT COALESCE(MAX(
			CASE
				WHEN task_id GLOB 'task-[0-9]*' THEN CAST(SUBSTR(task_id, 6) AS INTEGER)
				ELSE 0
			END
		), 0) AS max_id
		FROM runtime__task_log`,
	)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	maxID := int64FromValueDefault(rows[0]["max_id"])
	if maxID < 0 {
		return 0, nil
	}
	return uint64(maxID), nil
}

func (s *TaskLogState) ensureInitialized() error {
	if s == nil {
		return fmt.Errorf("task log state is nil")
	}
	if s.tc == nil {
		return fmt.Errorf("task log task context is not initialized")
	}
	return nil
}

func (s *TaskLogState) exec(query string, args ...any) error {
	_, err := s.tc.SqlExec(query, args...)
	return err
}

func (s *TaskLogState) query(query string, args ...any) ([]map[string]any, error) {
	res, err := s.tc.SqlQuery(query, args...)
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

func (s *TaskLogState) ensureColumn(tableName, columnName, definition string) error {
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
			quoteSQLiteIdentifier(tableName),
			quoteSQLiteIdentifier(columnName),
			definition,
		),
	)
	return err
}

func (s *TaskLogState) tableHasColumn(tableName, columnName string) (bool, error) {
	res, err := s.tc.SqlQuery(fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLiteIdentifier(tableName)))
	if err != nil {
		return false, err
	}
	for _, row := range res.Rows {
		name, _ := row["name"].(string)
		if name == columnName {
			return true, nil
		}
	}
	return false, nil
}

func boolToSQLiteInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func int64FromValueDefault(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func taskLogRecordFromRow(row map[string]any) (TaskLogRecord, error) {
	taskID, _ := row["task_id"].(string)
	functionID, _ := row["function_id"].(string)
	statusText, _ := row["status"].(string)
	errorText, _ := row["error_text"].(string)
	hideByDefault := int64FromValueDefault(row["hide_by_default"]) != 0

	createdAt, err := parseOptionalTimestamp(row["created_at"])
	if err != nil || createdAt == nil {
		return TaskLogRecord{}, fmt.Errorf("decode task %s created_at: %w", taskID, err)
	}
	startedAt, err := parseOptionalTimestamp(row["started_at"])
	if err != nil {
		return TaskLogRecord{}, fmt.Errorf("decode task %s started_at: %w", taskID, err)
	}
	finishedAt, err := parseOptionalTimestamp(row["finished_at"])
	if err != nil {
		return TaskLogRecord{}, fmt.Errorf("decode task %s finished_at: %w", taskID, err)
	}
	updatedAt, err := parseOptionalTimestamp(row["updated_at"])
	if err != nil || updatedAt == nil {
		return TaskLogRecord{}, fmt.Errorf("decode task %s updated_at: %w", taskID, err)
	}

	var output map[string]any
	if err := decodeJSONValue(row["output_json"], &output); err != nil {
		return TaskLogRecord{}, fmt.Errorf("decode task %s output_json: %w", taskID, err)
	}
	var input map[string]any
	if err := decodeJSONValue(row["input_json"], &input); err != nil {
		return TaskLogRecord{}, fmt.Errorf("decode task %s input_json: %w", taskID, err)
	}
	var conversation []baselineagent.ConversationMessage
	if err := decodeJSONValue(row["conversation_json"], &conversation); err != nil {
		return TaskLogRecord{}, fmt.Errorf("decode task %s conversation_json: %w", taskID, err)
	}

	return TaskLogRecord{
		TaskSnapshot: TaskSnapshot{
			TaskID:     taskID,
			FunctionID: functionID,
			Status:     TaskStatus(strings.TrimSpace(statusText)),
			CreatedAt:  *createdAt,
			StartedAt:  startedAt,
			FinishedAt: finishedAt,
			Output:     output,
			Error:      errorText,
		},
		Input:         input,
		Conversation:  conversation,
		HideByDefault: hideByDefault,
		UpdatedAt:     *updatedAt,
	}, nil
}

func parseOptionalTimestamp(v any) (*time.Time, error) {
	text, _ := v.(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil, err
	}
	return &ts, nil
}

func decodeJSONValue(v any, dst any) error {
	text, _ := v.(string)
	text = strings.TrimSpace(text)
	if text == "" || text == "null" {
		return nil
	}
	return json.Unmarshal([]byte(text), dst)
}

func quoteSQLiteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
