package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	convdb "zoa/conversation/db"
	"zoa/internal/semtrace"
	"zoa/internal/agentloop/llm"
	tools "zoa/internal/agentloop/tools"
)

type TaskStatus string

const (
	TaskStatusRunning  TaskStatus = "running"
	TaskStatusDone     TaskStatus = "done"
	TaskStatusFailed   TaskStatus = "failed"
	TaskStatusCanceled TaskStatus = "canceled"
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
	Input         map[string]any `json:"input,omitempty"`
	Conversation  []llm.Message  `json:"conversation,omitempty"`
	HideByDefault bool           `json:"hide_by_default,omitempty"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type TaskManagerOptions struct {
	// SQLitePath configures the runtime SQLite database path.
	// When set, TaskManager opens and owns this connection.
	SQLitePath string

	// UserSQLitePath configures the user-state SQLite database path for hub functions.
	// Defaults to a sibling state.db next to SQLitePath.
	UserSQLitePath string

	// ConversationSQLitePath configures the conversation SQLite database path.
	// Defaults to a sibling conversation.db next to SQLitePath.
	ConversationSQLitePath string

	// Logger is an optional structured logger. Defaults to slog.Default().
	Logger *slog.Logger
}

type SpawnOptions struct {
	// HideInLogByDefault marks this task as hidden from default task log views.
	HideInLogByDefault bool
	// TaskTimeout sets a per-task NL execution timeout.
	// When > 0, it overrides timeout_sec-derived defaults for this spawned task.
	TaskTimeout time.Duration
}

type taskRecord struct {
	TaskSnapshot
	spawnOptions SpawnOptions
	input        map[string]any
	conversation []llm.Message
	parentTaskID string
	cancelFunc   context.CancelFunc
	// cancelRequested handles the race where Cancel(taskID) happens after Spawn
	// returns but before runTask installs cancelFunc.
	cancelRequested bool
	done            chan struct{}
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
	registry       *Registry
	baseCtx        context.Context
	opts           TaskManagerOptions
	logger         *slog.Logger
	sqlDB          *sql.DB
	conversationDB *convdb.DB
	taskLog        *TaskLogState

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
	conversationPath := strings.TrimSpace(opts.ConversationSQLitePath)
	if conversationPath == "" {
		conversationPath = ConversationSQLitePath(resolvedPath)
	}
	userPath := strings.TrimSpace(opts.UserSQLitePath)
	if userPath == "" {
		userPath = UserSQLitePath(resolvedPath)
	}
	conversationDB, err := convdb.Open(conversationPath)
	if err != nil {
		manager.pumpCancel()
		_ = db.Close()
		return nil, err
	}
	manager.sqlDB = db
	manager.conversationDB = conversationDB
	manager.opts.SQLitePath = resolvedPath
	manager.opts.UserSQLitePath = userPath
	manager.opts.ConversationSQLitePath = conversationPath
	taskLogCtx, err := NewTaskContext(ctx, TaskContextOptions{
		SQLitePath:             userPath,
		RuntimeSQLitePath:      resolvedPath,
		ConversationSQLitePath: conversationPath,
		runtimeSQLDB:           db,
		conversationDB:         conversationDB,
	})
	if err != nil {
		manager.pumpCancel()
		_ = conversationDB.Close()
		_ = db.Close()
		return nil, err
	}
	manager.taskLog = LogState(taskLogCtx)
	if err := manager.taskLog.Init(); err != nil {
		manager.pumpCancel()
		_ = conversationDB.Close()
		_ = db.Close()
		return nil, err
	}
	maxTaskSeq, err := manager.taskLog.MaxTaskSequence()
	if err != nil {
		manager.pumpCancel()
		_ = conversationDB.Close()
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
	var convErr error
	if m.conversationDB != nil {
		convErr = m.conversationDB.Close()
	}
	if m.sqlDB == nil {
		return convErr
	}
	sqlErr := m.sqlDB.Close()
	if sqlErr != nil {
		return sqlErr
	}
	return convErr
}

func (m *TaskManager) Spawn(functionID string, input map[string]any, opts SpawnOptions) (string, error) {
	return m.spawnWithParent("", functionID, input, opts)
}

func (m *TaskManager) spawnWithParent(parentTaskID string, functionID string, input map[string]any, opts SpawnOptions) (string, error) {
	fn, mergedInput, err := m.resolveFunctionInput(functionID, input)
	if err != nil {
		return "", err
	}
	taskID, rec, err := m.newRecord(parentTaskID, functionID, mergedInput, opts)
	if err != nil {
		return "", err
	}
	_ = m.persistTask(taskID)

	if !opts.HideInLogByDefault {
		m.logger.Info("task spawned", "task_id", taskID, "function_id", functionID, "parent_task_id", strings.TrimSpace(parentTaskID))
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
	rec, ok, err := m.liveRecord(taskID)
	if err != nil {
		return TaskSnapshot{}, false, err
	}
	if !ok {
		snap, err := m.Get(taskID)
		return snap, false, err
	}

	if timeout <= 0 {
		<-rec.done
		return m.snapshotFromLiveRecord(rec), false, nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-rec.done:
		return m.snapshotFromLiveRecord(rec), false, nil
	case <-timer.C:
		return m.snapshotFromLiveRecord(rec), true, nil
	}
}

func (m *TaskManager) Get(taskID string) (TaskSnapshot, error) {
	rec, ok, err := m.liveRecord(taskID)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if !ok {
		record, err := m.taskLog.Get(taskID)
		if err != nil {
			return TaskSnapshot{}, err
		}
		record.Output = cloneMapAny(record.Output)
		return record.TaskSnapshot, nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	clone := rec.TaskSnapshot
	clone.Output = cloneMapAny(clone.Output)
	return clone, nil
}

func (m *TaskManager) snapshotFromLiveRecord(rec *taskRecord) TaskSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	clone := rec.TaskSnapshot
	clone.Output = cloneMapAny(clone.Output)
	return clone
}

func (m *TaskManager) Cancel(taskID string) (bool, error) {
	if strings.TrimSpace(taskID) == "" {
		return false, fmt.Errorf("task_id cannot be empty")
	}
	m.mu.Lock()
	_, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		snap, err := m.Get(taskID)
		if err != nil {
			return false, err
		}
		if snap.Status == TaskStatusRunning {
			return false, fmt.Errorf("unknown task_id: %s", taskID)
		}
		return false, nil
	}
	toCancel := make([]context.CancelFunc, 0)
	anyCanceled := false
	visitQueue := []string{taskID}
	visited := map[string]struct{}{}
	for len(visitQueue) > 0 {
		current := visitQueue[0]
		visitQueue = visitQueue[1:]
		if _, seen := visited[current]; seen {
			continue
		}
		visited[current] = struct{}{}

		rec, exists := m.tasks[current]
		if !exists {
			continue
		}
		select {
		case <-rec.done:
		default:
			rec.cancelRequested = true
			if rec.cancelFunc != nil {
				toCancel = append(toCancel, rec.cancelFunc)
			}
			anyCanceled = true
		}
		for childID, child := range m.tasks {
			if child == nil {
				continue
			}
			if child.parentTaskID == current {
				visitQueue = append(visitQueue, childID)
			}
		}
	}
	m.mu.Unlock()
	for _, cancel := range toCancel {
		cancel()
	}
	return anyCanceled, nil
}

func (m *TaskManager) runTask(fn *Function, rec *taskRecord, input map[string]any) {
	now := time.Now().UTC()
	m.mu.Lock()
	rec.StartedAt = &now
	taskID := rec.TaskID
	functionID := rec.FunctionID
	m.mu.Unlock()
	_ = m.persistTask(taskID)

	runCtx := m.baseCtx
	if runCtx == nil {
		runCtx = context.Background()
	}
	if rec.spawnOptions.TaskTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, rec.spawnOptions.TaskTimeout)
		defer cancel()
	}
	runCtx, taskCancel := context.WithCancel(runCtx)
	defer taskCancel()
	m.mu.Lock()
	rec.cancelFunc = taskCancel
	cancelRequested := rec.cancelRequested
	m.mu.Unlock()
	if cancelRequested {
		taskCancel()
	}
	runCtx, task := semtrace.NewTaskWithAttrs(runCtx, "TaskManager.runTask", map[string]any{
		"task_id":                  taskID,
		"function_id":              functionID,
		"hide_by_default":          rec.spawnOptions.HideInLogByDefault,
		"hide_from_log_by_default": rec.spawnOptions.HideInLogByDefault,
	})
	defer task.End()
	semtrace.LogAttrs(runCtx, "zoafunction", "task started", map[string]any{
		"task_id":                  taskID,
		"function_id":              functionID,
		"hide_by_default":          rec.spawnOptions.HideInLogByDefault,
		"hide_from_log_by_default": rec.spawnOptions.HideInLogByDefault,
	})

	m.logger.Debug("task started", "task_id", taskID, "function_id", functionID)
	execCtx, execRegion := semtrace.StartRegionWithAttrs(runCtx, "zoafunction.run", map[string]any{
		"task_id":                  taskID,
		"function_id":              functionID,
		"hide_from_log_by_default": rec.spawnOptions.HideInLogByDefault,
	})
	res, err := m.runFunction(execCtx, taskID, fn, input)
	execRegion.End()

	end := time.Now().UTC()
	m.mu.Lock()
	rec.FinishedAt = &end
	rec.cancelFunc = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			rec.Status = TaskStatusCanceled
			rec.Error = "task canceled"
		} else {
			rec.Status = TaskStatusFailed
			rec.Error = err.Error()
		}
	} else {
		rec.Status = TaskStatusDone
		rec.Output = cloneMapAny(res.Output)
	}
	status := rec.Status
	rec.conversation = cloneConversationMessages(res.Conversation)
	done := rec.done
	m.mu.Unlock()
	if status == TaskStatusCanceled {
		_, _ = m.Cancel(taskID)
	}
	_ = m.persistTask(taskID)
	if err != nil {
		semtrace.LogAttrs(runCtx, "zoafunction.error", err.Error(), map[string]any{
			"task_id":     taskID,
			"function_id": functionID,
		})
	}
	semtrace.LogAttrs(runCtx, "zoafunction", "task finished", map[string]any{
		"task_id":                  taskID,
		"function_id":              functionID,
		"status":                   string(status),
		"duration_ms":              end.Sub(now).Milliseconds(),
		"conversation_messages":    len(res.Conversation),
		"hide_from_log_by_default": rec.spawnOptions.HideInLogByDefault,
	})

	m.logger.Debug("task finished", "task_id", taskID, "function_id", functionID, "status", status, "duration", end.Sub(now))
	close(done)
	m.mu.Lock()
	delete(m.tasks, taskID)
	m.mu.Unlock()
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

func (m *TaskManager) runFunction(ctx context.Context, parentTaskID string, fn *Function, input map[string]any) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
	tcOpts.SQLitePath = m.opts.UserSQLitePath
	tcOpts.RuntimeSQLitePath = m.opts.SQLitePath
	tcOpts.ConversationSQLitePath = m.opts.ConversationSQLitePath
	tcOpts.runtimeSQLDB = m.sqlDB
	tcOpts.registerPump = m.registerPump
	tcOpts.spawnTask = func(functionID string, input map[string]any, opts SpawnOptions) (string, error) {
		return m.spawnWithParent(parentTaskID, functionID, input, opts)
	}
	tcOpts.zoaFunctionTools = m.newZoaFunctionTools
	tcOpts.loadMixin = m.registry.GetMixin
	tcOpts.Namespace = namespaceFromFunctionID(fn.ID)
	tcOpts.conversationDB = m.conversationDB
	tcOpts.taskID = parentTaskID
	if fn.AssetsDir != "" {
		tcOpts.AssetsDir = fn.AssetsDir
	}
	taskCtx, err := NewTaskContext(ctx, tcOpts)
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

func (m *TaskManager) newZoaFunctionTools() ([]tools.Tool, error) {
	if m == nil {
		return nil, fmt.Errorf("task manager is nil")
	}
	return newZoaFunctionTools(m.registry, m)
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

func (m *TaskManager) newRecord(parentTaskID string, functionID string, input map[string]any, opts SpawnOptions) (string, *taskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parentTaskID = strings.TrimSpace(parentTaskID)
	if parentTaskID != "" {
		parent, ok := m.tasks[parentTaskID]
		if !ok {
			return "", nil, fmt.Errorf("unknown parent_task_id: %s", parentTaskID)
		}
		if parent.cancelRequested {
			return "", nil, fmt.Errorf("parent task %s is canceled", parentTaskID)
		}
		select {
		case <-parent.done:
			return "", nil, fmt.Errorf("parent task %s is already complete", parentTaskID)
		default:
		}
	}
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
		parentTaskID: parentTaskID,
		done:         make(chan struct{}),
	}
	m.tasks[taskID] = rec
	return taskID, rec, nil
}

func (m *TaskManager) liveRecord(taskID string) (*taskRecord, bool, error) {
	if taskID == "" {
		return nil, false, fmt.Errorf("task_id cannot be empty")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.tasks[taskID]
	if !ok {
		return nil, false, nil
	}
	return rec, true, nil
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

// namespaceFromFunctionID extracts the namespace prefix from a dotted function ID.
// e.g. "gateway.recv" → "gateway", "md_to_pdf.md_to_pdf" → "md_to_pdf"
func namespaceFromFunctionID(id string) string {
	if i := strings.IndexByte(id, '.'); i > 0 {
		return id[:i]
	}
	return id
}

func cloneConversationMessages(in []llm.Message) []llm.Message {
	if in == nil {
		return []llm.Message{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make([]llm.Message, len(in))
		copy(out, in)
		return out
	}
	var out []llm.Message
	if err := json.Unmarshal(b, &out); err != nil {
		out = make([]llm.Message, len(in))
		copy(out, in)
		return out
	}
	return out
}
