package lmfrt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
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
	Input         map[string]any                      `json:"input,omitempty"`
	Conversation  []baselineagent.ConversationMessage `json:"conversation,omitempty"`
	HideByDefault bool                                `json:"hide_by_default,omitempty"`
	UpdatedAt     time.Time                           `json:"updated_at"`
}

type TaskManagerOptions struct {
	// SQLitePath configures the runtime SQLite database path.
	// When set, TaskManager opens and owns this connection.
	SQLitePath string

	// Logger is an optional structured logger. Defaults to slog.Default().
	Logger *slog.Logger
}

type SpawnOptions struct {
	// HideInLogByDefault marks this task as hidden from default task log views.
	HideInLogByDefault bool
}

type taskRecord struct {
	TaskSnapshot
	spawnOptions SpawnOptions
	input        map[string]any
	conversation []baselineagent.ConversationMessage
	done         chan struct{}
}

type pumpRunner struct {
	id         string
	functionID string
	input      map[string]any
	interval   time.Duration
	cancel     context.CancelFunc
	done       chan struct{}
}

type TaskManager struct {
	registry *Registry
	baseCtx  context.Context
	opts     TaskManagerOptions
	logger   *slog.Logger
	sqlDB    *sql.DB
	taskLog  *TaskLogState

	mu     sync.RWMutex
	nextID uint64
	tasks  map[string]*taskRecord

	initMu     sync.Mutex
	initCalled bool

	pumpCtx    context.Context
	pumpCancel context.CancelFunc
	pumpMu     sync.Mutex
	pumps      map[string]*pumpRunner
	closed     bool
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
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "task_manager")

	manager := &TaskManager{
		registry: registry,
		baseCtx:  ctx,
		opts:     opts,
		logger:   logger,
		tasks:    map[string]*taskRecord{},
		pumps:    map[string]*pumpRunner{},
	}
	manager.pumpCtx, manager.pumpCancel = context.WithCancel(ctx)
	db, resolvedPath, err := openSQLite(opts.SQLitePath)
	if err != nil {
		manager.pumpCancel()
		return nil, err
	}
	manager.sqlDB = db
	manager.opts.SQLitePath = resolvedPath
	taskLogCtx, err := NewTaskContext(ctx, TaskContextOptions{
		SQLitePath: resolvedPath,
		sqlDB:      db,
	})
	if err != nil {
		manager.pumpCancel()
		_ = db.Close()
		return nil, err
	}
	manager.taskLog = LogState(taskLogCtx)
	if err := manager.taskLog.Init(); err != nil {
		manager.pumpCancel()
		_ = db.Close()
		return nil, err
	}
	maxTaskSeq, err := manager.taskLog.MaxTaskSequence()
	if err != nil {
		manager.pumpCancel()
		_ = db.Close()
		return nil, err
	}
	manager.nextID = maxTaskSeq
	logger.Info("task manager started", "sqlite_path", resolvedPath)
	return manager, nil
}

func (m *TaskManager) Close() error {
	if m == nil {
		return nil
	}
	m.logger.Info("task manager closing")
	m.pumpMu.Lock()
	if m.closed {
		m.pumpMu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.pumpCancel
	m.pumpMu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.stopAllPumps()
	if m.sqlDB == nil {
		return nil
	}
	return m.sqlDB.Close()
}

func (m *TaskManager) Spawn(functionID string, input map[string]any, opts SpawnOptions) (string, error) {
	fn, mergedInput, err := m.resolveFunctionInput(functionID, input)
	if err != nil {
		return "", err
	}
	taskID, rec := m.newRecord(functionID, mergedInput, opts)
	_ = m.persistTask(taskID)

	if !opts.HideInLogByDefault {
		m.logger.Info("task spawned", "task_id", taskID, "function_id", functionID)
	}
	go m.runTask(fn, rec, mergedInput)
	return taskID, nil
}

func (m *TaskManager) Init() error {
	if m == nil {
		return fmt.Errorf("task manager is nil")
	}
	if m.registry == nil {
		return fmt.Errorf("task manager registry is nil")
	}
	m.initMu.Lock()
	if m.initCalled {
		m.initMu.Unlock()
		return fmt.Errorf("task manager init already called")
	}
	m.initCalled = true
	m.initMu.Unlock()
	m.logger.Info("running init functions")
	for _, fn := range m.registry.List() {
		if !strings.HasSuffix(fn.ID, ".__init__") {
			continue
		}
		taskID, err := m.Spawn(fn.ID, map[string]any{}, SpawnOptions{})
		if err != nil {
			return fmt.Errorf("spawn init function %q: %w", fn.ID, err)
		}
		snapshot, _, err := m.Wait(taskID, 0)
		if err != nil {
			return fmt.Errorf("wait init function %q: %w", fn.ID, err)
		}
		if snapshot.Status == TaskStatusFailed {
			initErr := strings.TrimSpace(snapshot.Error)
			if initErr == "" {
				initErr = "unknown error"
			}
			return fmt.Errorf("run init function %q: %s", fn.ID, initErr)
		}
		if snapshot.Status != TaskStatusDone {
			return fmt.Errorf("run init function %q: unexpected status %q", fn.ID, snapshot.Status)
		}
	}
	return nil
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
	taskID := rec.TaskID
	functionID := rec.FunctionID
	m.mu.Unlock()
	_ = m.persistTask(taskID)

	m.logger.Debug("task started", "task_id", taskID, "function_id", functionID)
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
	status := rec.Status
	rec.conversation = cloneConversationMessages(res.Conversation)
	done := rec.done
	m.mu.Unlock()
	_ = m.persistTask(taskID)

	m.logger.Debug("task finished", "task_id", taskID, "function_id", functionID, "status", status, "duration", end.Sub(now))
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
	tcOpts.logger = m.logger
	tcOpts.SQLitePath = m.opts.SQLitePath
	tcOpts.sqlDB = m.sqlDB
	tcOpts.registerPump = m.registerPump
	tcOpts.spawnTask = m.Spawn
	tcOpts.lmfTools = m.newLMFunctionTools
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

func (m *TaskManager) registerPump(pumpID, functionID string, input map[string]any, interval time.Duration) error {
	if m == nil {
		return fmt.Errorf("task manager is nil")
	}
	if strings.TrimSpace(pumpID) == "" {
		return fmt.Errorf("pump_id cannot be empty")
	}
	if strings.TrimSpace(functionID) == "" {
		return fmt.Errorf("function_id cannot be empty")
	}
	if interval <= 0 {
		return fmt.Errorf("interval must be > 0")
	}
	if _, ok := m.registry.Get(functionID); !ok {
		return fmt.Errorf("unknown function: %s", functionID)
	}

	pumpID = strings.TrimSpace(pumpID)
	functionID = strings.TrimSpace(functionID)
	input = cloneMapAny(input)

	m.pumpMu.Lock()
	if m.closed {
		m.pumpMu.Unlock()
		return fmt.Errorf("task manager is closed")
	}
	existing, exists := m.pumps[pumpID]
	if exists &&
		existing.functionID == functionID &&
		existing.interval == interval &&
		reflect.DeepEqual(existing.input, input) {
		m.pumpMu.Unlock()
		return nil
	}

	var toStop *pumpRunner
	if exists {
		toStop = existing
		delete(m.pumps, pumpID)
	}

	pumpCtx, cancel := context.WithCancel(m.pumpCtx)
	runner := &pumpRunner{
		id:         pumpID,
		functionID: functionID,
		input:      cloneMapAny(input),
		interval:   interval,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	m.pumps[pumpID] = runner
	m.pumpMu.Unlock()

	m.logger.Info("pump registered", "pump_id", pumpID, "function_id", functionID, "interval", interval)
	if toStop != nil {
		toStop.cancel()
		<-toStop.done
	}

	go m.runPumpLoop(pumpCtx, runner)
	return nil
}

func (m *TaskManager) newLMFunctionTools() ([]baselineagent.Tool, error) {
	if m == nil {
		return nil, fmt.Errorf("task manager is nil")
	}
	return newLMFunctionTools(m.registry, m)
}

func (m *TaskManager) stopAllPumps() {
	m.pumpMu.Lock()
	m.logger.Debug("stopping all pumps", "count", len(m.pumps))
	pumps := make([]*pumpRunner, 0, len(m.pumps))
	for id, runner := range m.pumps {
		delete(m.pumps, id)
		pumps = append(pumps, runner)
	}
	m.pumpMu.Unlock()

	for _, runner := range pumps {
		runner.cancel()
	}
	for _, runner := range pumps {
		<-runner.done
	}
}

func (m *TaskManager) runPumpLoop(ctx context.Context, runner *pumpRunner) {
	defer close(runner.done)
	if runner == nil {
		return
	}

	runOnce := func() {
		if err := m.runPumpOnce(runner); err != nil {
			m.logger.Error("pump run error", "pump", runner.id, "error", err)
		}
	}

	runOnce()
	ticker := time.NewTicker(runner.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

func (m *TaskManager) runPumpOnce(runner *pumpRunner) error {
	if runner == nil {
		return fmt.Errorf("pump runner is nil")
	}
	taskID, err := m.Spawn(runner.functionID, cloneMapAny(runner.input), SpawnOptions{
		HideInLogByDefault: true,
	})
	if err != nil {
		return err
	}
	snapshot, _, err := m.Wait(taskID, 0)
	if err != nil {
		return err
	}
	if snapshot.Status == TaskStatusFailed {
		if strings.TrimSpace(snapshot.Error) == "" {
			return fmt.Errorf("pump task %s failed", taskID)
		}
		return fmt.Errorf("pump task %s failed: %s", taskID, snapshot.Error)
	}
	return nil
}

func (m *TaskManager) newRecord(functionID string, input map[string]any, opts SpawnOptions) (string, *taskRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	taskID := fmt.Sprintf("task-%d", m.nextID)
	now := time.Now().UTC()
	rec := &taskRecord{
		TaskSnapshot: TaskSnapshot{
			TaskID:     taskID,
			FunctionID: functionID,
			Status:     TaskStatusRunning,
			CreatedAt:  now,
		},
		spawnOptions: opts,
		input:        cloneMapAny(input),
		done:         make(chan struct{}),
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
	record, err := m.taskLogRecord(taskID)
	if err != nil {
		return err
	}
	if m.taskLog == nil {
		return fmt.Errorf("task log store is not initialized")
	}
	return m.taskLog.upsert(record)
}

func (m *TaskManager) taskLogRecord(taskID string) (TaskLogRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.tasks[taskID]
	if !ok {
		return TaskLogRecord{}, fmt.Errorf("unknown task_id: %s", taskID)
	}
	out := TaskLogRecord{
		TaskSnapshot:  rec.TaskSnapshot,
		Input:         cloneMapAny(rec.input),
		Conversation:  cloneConversationMessages(rec.conversation),
		HideByDefault: rec.spawnOptions.HideInLogByDefault,
		UpdatedAt:     time.Now().UTC(),
	}
	out.Output = cloneMapAny(out.Output)
	return out, nil
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
