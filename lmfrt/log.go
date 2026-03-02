package lmfrt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type TaskLogSummary struct {
	TaskID     string
	FunctionID string
	Status     TaskStatus
	Error      string
	UpdatedAt  string
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
	if err := s.exec(`CREATE TABLE IF NOT EXISTS lmfrt__task_log (
		task_id TEXT PRIMARY KEY,
		function_id TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		started_at TEXT,
		finished_at TEXT,
		output_json TEXT,
		error_text TEXT,
		input_json TEXT,
		conversation_json TEXT,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create lmfrt__task_log table: %w", err)
	}
	if err := s.exec(`CREATE INDEX IF NOT EXISTS lmfrt__task_log_status_updated_idx ON lmfrt__task_log(status, updated_at DESC)`); err != nil {
		return fmt.Errorf("create lmfrt__task_log index: %w", err)
	}
	return nil
}

func (s *TaskLogState) Summaries(limit int, onlyRunning bool) ([]TaskLogSummary, error) {
	if err := s.ensureInitialized(); err != nil {
		return nil, err
	}

	query := `SELECT task_id, function_id, status, error_text, updated_at
		FROM lmfrt__task_log`
	args := []any{}
	if onlyRunning {
		query += ` WHERE status = ?`
		args = append(args, string(TaskStatusRunning))
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
		items = append(items, TaskLogSummary{
			TaskID:     taskID,
			FunctionID: functionID,
			Status:     TaskStatus(strings.TrimSpace(statusText)),
			Error:      errorText,
			UpdatedAt:  updatedAt,
		})
	}
	return items, nil
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
		`INSERT INTO lmfrt__task_log(
			task_id, function_id, status, created_at, started_at, finished_at,
			output_json, error_text, input_json, conversation_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			function_id = excluded.function_id,
			status = excluded.status,
			created_at = excluded.created_at,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at,
			output_json = excluded.output_json,
			error_text = excluded.error_text,
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
		string(inputJSON),
		string(conversationJSON),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("upsert lmfrt task log: %w", err)
	}
	return nil
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
