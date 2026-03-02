package lmfrt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	baselineagent "zoa/baselineagent"
)

type TaskStatus string

const (
	TaskStatusRunning TaskStatus = "running"
	TaskStatusDone    TaskStatus = "done"
	TaskStatusFailed  TaskStatus = "failed"
)

type TaskSnapshot struct {
	TaskID     string         `json:"task_id"`
	FunctionID string         `json:"function_id"`
	Status     TaskStatus     `json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type TaskLogRecord struct {
	TaskSnapshot
	Input        map[string]any                      `json:"input,omitempty"`
	Conversation []baselineagent.ConversationMessage `json:"conversation,omitempty"`
	UpdatedAt    time.Time                           `json:"updated_at"`
}

type TaskManagerOptions struct {
	// TaskLogDir writes one JSON file per task when set.
	TaskLogDir string
	// SQLitePath configures the runtime SQLite database path.
	// When set, TaskManager opens and owns this connection.
	SQLitePath string
}

type taskRecord struct {
	TaskSnapshot
	input        map[string]any
	conversation []baselineagent.ConversationMessage
	done         chan struct{}
}

type TaskManager struct {
	registry *Registry
	baseCtx  context.Context
	opts     TaskManagerOptions
	sqlDB    *sql.DB

	mu     sync.RWMutex
	nextID uint64
	tasks  map[string]*taskRecord
}

func NewTaskManager(registry *Registry, opts TaskManagerOptions) (*TaskManager, error) {
	return NewTaskManagerWithContext(context.Background(), registry, opts)
}

func NewTaskManagerWithContext(ctx context.Context, registry *Registry, opts TaskManagerOptions) (*TaskManager, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(opts.SQLitePath) == "" {
		return nil, fmt.Errorf("sqlite path is required for task manager")
	}
	manager := &TaskManager{
		registry: registry,
		baseCtx:  ctx,
		opts:     opts,
		tasks:    map[string]*taskRecord{},
	}
	db, resolvedPath, err := openSQLite(opts.SQLitePath)
	if err != nil {
		return nil, err
	}
	manager.sqlDB = db
	manager.opts.SQLitePath = resolvedPath
	return manager, nil
}

func (m *TaskManager) Close() error {
	if m == nil || m.sqlDB == nil {
		return nil
	}
	return m.sqlDB.Close()
}

func (m *TaskManager) Spawn(functionID string, input map[string]any) (string, error) {
	fn, mergedInput, err := m.resolveFunctionInput(functionID, input)
	if err != nil {
		return "", err
	}
	taskID, rec := m.newRecord(functionID, mergedInput)
	_ = m.persistTask(taskID)

	go m.runTask(fn, rec, mergedInput)
	return taskID, nil
}

func (m *TaskManager) Run(functionID string, input map[string]any) (RunResult, error) {
	fn, mergedInput, err := m.resolveFunctionInput(functionID, input)
	if err != nil {
		return RunResult{}, err
	}
	return m.runFunction(fn, mergedInput)
}

func (m *TaskManager) Wait(taskID string, timeout time.Duration) (TaskSnapshot, bool, error) {
	rec, err := m.getRecord(taskID)
	if err != nil {
		return TaskSnapshot{}, false, err
	}

	if timeout <= 0 {
		<-rec.done
		snap, getErr := m.Get(taskID)
		return snap, false, getErr
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-rec.done:
		snap, getErr := m.Get(taskID)
		return snap, false, getErr
	case <-timer.C:
		snap, getErr := m.Get(taskID)
		return snap, true, getErr
	}
}

func (m *TaskManager) Get(taskID string) (TaskSnapshot, error) {
	rec, err := m.getRecord(taskID)
	if err != nil {
		return TaskSnapshot{}, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	clone := rec.TaskSnapshot
	clone.Output = cloneMapAny(clone.Output)
	return clone, nil
}

func (m *TaskManager) runTask(fn *Function, rec *taskRecord, input map[string]any) {
	now := time.Now().UTC()
	m.mu.Lock()
	rec.StartedAt = &now
	m.mu.Unlock()
	_ = m.persistTask(rec.TaskID)

	res, err := m.runFunction(fn, input)

	end := time.Now().UTC()
	m.mu.Lock()
	rec.FinishedAt = &end
	if err != nil {
		rec.Status = TaskStatusFailed
		rec.Error = err.Error()
	} else {
		rec.Status = TaskStatusDone
		rec.Output = cloneMapAny(res.Output)
	}
	rec.conversation = cloneConversationMessages(res.Conversation)
	done := rec.done
	taskID := rec.TaskID
	m.mu.Unlock()
	_ = m.persistTask(taskID)
	close(done)
}

func (m *TaskManager) resolveFunctionInput(functionID string, input map[string]any) (*Function, map[string]any, error) {
	if m == nil {
		return nil, nil, fmt.Errorf("task manager is nil")
	}
	if m.registry == nil {
		return nil, nil, fmt.Errorf("task manager registry is nil")
	}
	fn, ok := m.registry.Get(functionID)
	if !ok {
		return nil, nil, fmt.Errorf("unknown function: %s", functionID)
	}
	return fn, cloneMapAny(input), nil
}

func (m *TaskManager) runFunction(fn *Function, input map[string]any) (RunResult, error) {
	if fn == nil {
		return RunResult{}, fmt.Errorf("function is nil")
	}
	if fn.Exec == nil {
		return RunResult{}, fmt.Errorf("function %q has nil Exec", fn.ID)
	}
	if input == nil {
		input = map[string]any{}
	}

	tcOpts := taskContextOptionsFromInput(input)
	tcOpts.SQLitePath = m.opts.SQLitePath
	tcOpts.sqlDB = m.sqlDB
	taskCtx, err := NewTaskContext(m.baseCtx, tcOpts)
	if err != nil {
		return RunResult{}, err
	}
	defer func() {
		_ = taskCtx.Close()
	}()

	result := RunResult{FunctionID: fn.ID}
	output, err := fn.Exec(taskCtx, input)
	result.Conversation = taskCtx.conversationHistory()
	if output != nil {
		result.Output = output
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (m *TaskManager) newRecord(functionID string, input map[string]any) (string, *taskRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	taskID := fmt.Sprintf("task-%06d", m.nextID)
	now := time.Now().UTC()
	rec := &taskRecord{
		TaskSnapshot: TaskSnapshot{
			TaskID:     taskID,
			FunctionID: functionID,
			Status:     TaskStatusRunning,
			CreatedAt:  now,
		},
		input: cloneMapAny(input),
		done:  make(chan struct{}),
	}
	m.tasks[taskID] = rec
	return taskID, rec
}

func (m *TaskManager) getRecord(taskID string) (*taskRecord, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task_id cannot be empty")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("unknown task_id: %s", taskID)
	}
	return rec, nil
}

func (m *TaskManager) persistTask(taskID string) error {
	taskDir := strings.TrimSpace(m.opts.TaskLogDir)
	if taskDir == "" {
		return nil
	}
	record, err := m.taskLogRecord(taskID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return fmt.Errorf("create task log dir: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task log: %w", err)
	}
	target := filepath.Join(taskDir, taskFileName(record.TaskID))
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write task log temp: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("replace task log: %w", err)
	}
	return nil
}

func (m *TaskManager) taskLogRecord(taskID string) (TaskLogRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.tasks[taskID]
	if !ok {
		return TaskLogRecord{}, fmt.Errorf("unknown task_id: %s", taskID)
	}
	out := TaskLogRecord{
		TaskSnapshot: rec.TaskSnapshot,
		Input:        cloneMapAny(rec.input),
		Conversation: cloneConversationMessages(rec.conversation),
		UpdatedAt:    time.Now().UTC(),
	}
	out.Output = cloneMapAny(out.Output)
	return out, nil
}

func taskFileName(taskID string) string {
	trimmed := strings.TrimSpace(taskID)
	trimmed = strings.TrimPrefix(trimmed, "task-")
	if trimmed == "" {
		trimmed = "unknown"
	}
	return fmt.Sprintf("task-%s.json", trimmed)
}

func cloneMapAny(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func taskContextOptionsFromInput(input map[string]any) TaskContextOptions {
	opts := TaskContextOptions{}
	if v, ok := input["cwd"].(string); ok {
		opts.CWD = strings.TrimSpace(v)
	}
	if v, ok := input["model"].(string); ok {
		opts.Model = strings.TrimSpace(v)
	}
	if v, ok := numberAsInt(input["max_turns"]); ok {
		opts.MaxTurns = v
	}
	if v, ok := numberAsInt(input["timeout_sec"]); ok && v > 0 {
		opts.Timeout = time.Duration(v) * time.Second
	}
	if v, ok := numberAsFloat(input["temperature"]); ok {
		opts.Temperature = v
	}
	return opts
}

func numberAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func numberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMapAny(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = cloneAny(t[i])
		}
		return out
	default:
		return v
	}
}

func cloneConversationMessages(in []baselineagent.ConversationMessage) []baselineagent.ConversationMessage {
	if in == nil {
		return []baselineagent.ConversationMessage{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make([]baselineagent.ConversationMessage, len(in))
		copy(out, in)
		return out
	}
	var out []baselineagent.ConversationMessage
	if err := json.Unmarshal(b, &out); err != nil {
		out = make([]baselineagent.ConversationMessage, len(in))
		copy(out, in)
		return out
	}
	return out
}
