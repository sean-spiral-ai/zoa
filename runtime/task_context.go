package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	convdb "zoa/conversation/db"
	convrunner "zoa/conversation/runner"
	"zoa/internal/agentloop/llm"
	modelpkg "zoa/internal/agentloop/model"
	tools "zoa/internal/agentloop/tools"
)

const defaultConversationSystemPrompt = `You are a reliable assistant.
Use tools when needed, do not invent results, and keep responses concise.`

type taskConversationConfig struct {
	Model        string
	MaxTurns     int
	Timeout      time.Duration
	Temperature  float64
	SystemPrompt string
	Tools        []tools.Tool
}

type TaskContextOptions struct {
	APIKey                 string
	CWD                    string
	Model                  string
	MaxTurns               int
	Timeout                time.Duration
	Temperature            float64
	SQLitePath             string
	RuntimeSQLitePath      string
	ConversationSQLitePath string
	Namespace              string
	AssetsDir              string

	logger           *slog.Logger
	sqlDB            sqlExecutor
	runtimeSQLDB     sqlExecutor
	registerPump     func(pumpID, functionID string, input map[string]any, interval time.Duration) error
	spawnTask        func(functionID string, input map[string]any, opts SpawnOptions) (string, error)
	zoaFunctionTools func() ([]tools.Tool, error)
	loadMixin        func(id string) (*Mixin, bool)
	conversationDB   *convdb.DB
	taskID           string
}

type TaskContext struct {
	ctx                    context.Context
	logger                 *slog.Logger
	apiKey                 string
	baseConfig             taskConversationConfig
	conversationDB         *convdb.DB
	ownsConversationDB     bool
	sqlDB                  sqlExecutor
	ownsSQL                bool
	runtimeSQLDB           sqlExecutor
	ownsRuntimeSQL         bool
	namespace              string
	sqlitePath             string
	runtimeSQLitePath      string
	conversationSQLitePath string
	assetsDir              string
	tmpDirs                []string
	registerPump           func(pumpID, functionID string, input map[string]any, interval time.Duration) error
	spawnTask              func(functionID string, input map[string]any, opts SpawnOptions) (string, error)
	zoaFunctionTools       func() ([]tools.Tool, error)
	loadMixin              func(id string) (*Mixin, bool)
	taskID                 string
}

type SqlExecResult struct {
	RowsAffected int64  `json:"rows_affected"`
	LastInsertID *int64 `json:"last_insert_id,omitempty"`
}

type SqlQueryResult struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

func NewTaskContext(ctx context.Context, opts TaskContextOptions) (*TaskContext, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cwd := strings.TrimSpace(opts.CWD)
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve cwd: %w", err)
		}
		cwd = wd
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute cwd: %w", err)
	}

	toolset, err := tools.NewCodingTools(absCWD)
	if err != nil {
		return nil, fmt.Errorf("initialize baseline tools: %w", err)
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = modelpkg.DefaultModel
	}
	if !modelpkg.IsSupportedModel(model) {
		return nil, fmt.Errorf("unsupported model %q (supported: %s)", model, strings.Join(modelpkg.SupportedModelNames(), ", "))
	}

	baseConfig := taskConversationConfig{
		Model:       model,
		MaxTurns:    opts.MaxTurns,
		Timeout:     opts.Timeout,
		Temperature: opts.Temperature,
		Tools:       toolset,
	}
	apiKey, _ := modelpkg.ResolveCredential(opts.APIKey, model)
	sqlDB := opts.sqlDB
	ownsSQL := false
	if sqlDB == nil && strings.TrimSpace(opts.SQLitePath) != "" {
		db, _, err := openSQLite(opts.SQLitePath)
		if err != nil {
			return nil, err
		}
		sqlDB = db
		ownsSQL = true
	}
	if sqlDB == nil {
		return nil, fmt.Errorf("sqlite is required for task context")
	}
	runtimeSQLDB := opts.runtimeSQLDB
	ownsRuntimeSQL := false
	if runtimeSQLDB == nil && strings.TrimSpace(opts.RuntimeSQLitePath) != "" {
		db, _, err := openSQLite(opts.RuntimeSQLitePath)
		if err != nil {
			if ownsSQL {
				_ = sqlDB.Close()
			}
			return nil, err
		}
		runtimeSQLDB = db
		ownsRuntimeSQL = true
	}
	if runtimeSQLDB == nil {
		runtimeSQLDB = sqlDB
	}
	conversationSQLitePath := strings.TrimSpace(opts.ConversationSQLitePath)
	if conversationSQLitePath == "" {
		conversationSQLitePath = ConversationSQLitePath(firstNonEmpty(opts.RuntimeSQLitePath, opts.SQLitePath))
	}

	tcLogger := opts.logger
	if tcLogger == nil {
		tcLogger = slog.Default()
	}
	tcLogger = tcLogger.With("component", "task_context")

	return &TaskContext{
		ctx:                    ctx,
		logger:                 tcLogger,
		apiKey:                 apiKey,
		baseConfig:             baseConfig,
		sqlDB:                  sqlDB,
		ownsSQL:                ownsSQL,
		runtimeSQLDB:           runtimeSQLDB,
		ownsRuntimeSQL:         ownsRuntimeSQL,
		namespace:              opts.Namespace,
		sqlitePath:             opts.SQLitePath,
		runtimeSQLitePath:      opts.RuntimeSQLitePath,
		conversationSQLitePath: conversationSQLitePath,
		assetsDir:              opts.AssetsDir,
		registerPump:           opts.registerPump,
		spawnTask:              opts.spawnTask,
		zoaFunctionTools:       opts.zoaFunctionTools,
		loadMixin:              opts.loadMixin,
		conversationDB:         opts.conversationDB,
		taskID:                 strings.TrimSpace(opts.taskID),
	}, nil
}

func (t *TaskContext) Context() context.Context {
	return t.ctx
}

func (t *TaskContext) Close() error {
	for _, dir := range t.tmpDirs {
		_ = os.RemoveAll(dir)
	}
	t.tmpDirs = nil
	var outErr error
	if t.ownsSQL && t.sqlDB != nil {
		if err := t.sqlDB.Close(); err != nil {
			outErr = err
		}
	}
	if t.ownsRuntimeSQL && t.runtimeSQLDB != nil && t.runtimeSQLDB != t.sqlDB {
		if err := t.runtimeSQLDB.Close(); err != nil && outErr == nil {
			outErr = err
		}
	}
	if t.ownsConversationDB && t.conversationDB != nil {
		if err := t.conversationDB.Close(); err != nil && outErr == nil {
			outErr = err
		}
	}
	return outErr
}

// GetStateDir returns a persistent state directory for this namespace,
// located at <dir-of-state.db>/namespace_state/<namespace>/.
// Creates the directory if it doesn't exist.
func (t *TaskContext) GetStateDir() (string, error) {
	if t.namespace == "" {
		return "", fmt.Errorf("namespace is not set on this task context")
	}
	if t.sqlitePath == "" {
		return "", fmt.Errorf("sqlite path is not set on this task context")
	}
	dir := filepath.Join(filepath.Dir(t.sqlitePath), "namespace_state", t.namespace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create state dir: %w", err)
	}
	return dir, nil
}

// GetTmpDir creates a new temporary directory and registers it for cleanup on Close().
func (t *TaskContext) GetTmpDir() (string, error) {
	dir, err := os.MkdirTemp("", "zoa-runtime-"+t.namespace+"-*")
	if err != nil {
		return "", fmt.Errorf("create tmp dir: %w", err)
	}
	t.tmpDirs = append(t.tmpDirs, dir)
	return dir, nil
}

// GetAssetsDir returns the assets directory path for this namespace.
func (t *TaskContext) GetAssetsDir() (string, error) {
	if t.assetsDir == "" {
		return "", fmt.Errorf("assets dir is not configured for this task context")
	}
	return t.assetsDir, nil
}

func (t *TaskContext) ConversationDB() (*convdb.DB, error) {
	if t.conversationDB != nil {
		return t.conversationDB, nil
	}
	if strings.TrimSpace(t.conversationSQLitePath) == "" {
		return nil, fmt.Errorf("conversation sqlite path is not configured for this task context")
	}
	db, err := convdb.Open(t.conversationSQLitePath)
	if err != nil {
		return nil, err
	}
	t.conversationDB = db
	t.ownsConversationDB = true
	return db, nil
}

func (t *TaskContext) SqlExec(query string, args ...any) (SqlExecResult, error) {
	t.logger.Debug("sql exec", "query", truncate(query, 100))
	db, err := t.requireSQL()
	if err != nil {
		return SqlExecResult{}, err
	}
	res, err := db.ExecContext(t.ctx, query, args...)
	if err != nil {
		return SqlExecResult{}, err
	}
	out := SqlExecResult{}
	if rowsAffected, err := res.RowsAffected(); err == nil {
		out.RowsAffected = rowsAffected
	}
	if lastInsertID, err := res.LastInsertId(); err == nil {
		out.LastInsertID = &lastInsertID
	}
	return out, nil
}

func (t *TaskContext) SqlQuery(query string, args ...any) (SqlQueryResult, error) {
	t.logger.Debug("sql query", "query", truncate(query, 100))
	db, err := t.requireSQL()
	if err != nil {
		return SqlQueryResult{}, err
	}
	rows, err := db.QueryContext(t.ctx, query, args...)
	if err != nil {
		return SqlQueryResult{}, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return SqlQueryResult{}, err
	}
	out := SqlQueryResult{
		Columns: append([]string(nil), cols...),
		Rows:    make([]map[string]any, 0),
	}

	values := make([]any, len(cols))
	scanArgs := make([]any, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return SqlQueryResult{}, err
		}
		item := make(map[string]any, len(cols))
		for i, name := range cols {
			item[name] = normalizeSQLValue(values[i])
		}
		out.Rows = append(out.Rows, item)
	}
	if err := rows.Err(); err != nil {
		return SqlQueryResult{}, err
	}
	t.logger.Debug("sql query completed", "query", truncate(query, 100), "rows", len(out.Rows))
	return out, nil
}

func (t *TaskContext) SqlTx(fn func(*sql.Tx) error) error {
	if fn == nil {
		return fmt.Errorf("transaction callback cannot be nil")
	}
	db, err := t.requireSQL()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(t.ctx, nil)
	if err != nil {
		return err
	}
	t.logger.Debug("sql tx started")
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		t.logger.Debug("sql tx rolled back", "error", err)
		return err
	}
	t.logger.Debug("sql tx completed")
	return tx.Commit()
}

func (t *TaskContext) RegisterPump(pumpID, functionID string, input map[string]any, interval time.Duration) error {
	if t == nil {
		return fmt.Errorf("task context is nil")
	}
	if t.registerPump == nil {
		return fmt.Errorf("pump registration is unavailable for this task context")
	}
	pumpID = strings.TrimSpace(pumpID)
	if pumpID == "" {
		return fmt.Errorf("pump_id cannot be empty")
	}
	functionID = strings.TrimSpace(functionID)
	if functionID == "" {
		return fmt.Errorf("function_id cannot be empty")
	}
	if interval <= 0 {
		return fmt.Errorf("interval must be > 0")
	}
	t.logger.Debug("register pump", "pump_id", pumpID, "function_id", functionID, "interval", interval)
	return t.registerPump(pumpID, functionID, cloneMapAny(input), interval)
}

func (t *TaskContext) Spawn(functionID string, input map[string]any, opts SpawnOptions) (string, error) {
	if t == nil {
		return "", fmt.Errorf("task context is nil")
	}
	if t.spawnTask == nil {
		return "", fmt.Errorf("spawn is unavailable for this task context")
	}
	functionID = strings.TrimSpace(functionID)
	if functionID == "" {
		return "", fmt.Errorf("function_id cannot be empty")
	}
	t.logger.Debug("spawn task", "function_id", functionID)
	return t.spawnTask(functionID, cloneMapAny(input), opts)
}

func (t *TaskContext) NewZoaFunctionTools() ([]tools.Tool, error) {
	if t == nil {
		return nil, fmt.Errorf("task context is nil")
	}
	if t.zoaFunctionTools == nil {
		return nil, fmt.Errorf("zoafunction tools are unavailable for this task context")
	}
	return t.zoaFunctionTools()
}

func (t *TaskContext) LoadMixin(id string) error {
	if t == nil {
		return fmt.Errorf("task context is nil")
	}
	if t.loadMixin == nil {
		return fmt.Errorf("mixin loading is unavailable for this task context")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("mixin id cannot be empty")
	}
	mixin, ok := t.loadMixin(id)
	if !ok || mixin == nil {
		return fmt.Errorf("unknown mixin: %s", id)
	}
	if err := t.ensureMainConversation(); err != nil {
		return err
	}
	if err := t.appendToMainRef(llm.Message{
		Role: llm.RoleUser,
		Text: strings.TrimSpace(mixin.Content),
	}); err != nil {
		return err
	}
	if t.logger != nil {
		t.logger.Debug("appended mixin to conversation context", "mixin_id", mixin.ID)
	}
	return nil
}

func (t *TaskContext) requireSQL() (sqlExecutor, error) {
	if t.sqlDB == nil {
		return nil, fmt.Errorf("sqlite is not configured for this task context")
	}
	return t.sqlDB, nil
}

func (t *TaskContext) requireRuntimeSQL() (sqlExecutor, error) {
	if t.runtimeSQLDB == nil {
		return nil, fmt.Errorf("runtime sqlite is not configured for this task context")
	}
	return t.runtimeSQLDB, nil
}

func normalizeSQLValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (t *TaskContext) runtimeSqlExec(query string, args ...any) (SqlExecResult, error) {
	db, err := t.requireRuntimeSQL()
	if err != nil {
		return SqlExecResult{}, err
	}
	res, err := db.ExecContext(t.ctx, query, args...)
	if err != nil {
		return SqlExecResult{}, err
	}
	out := SqlExecResult{}
	if rowsAffected, err := res.RowsAffected(); err == nil {
		out.RowsAffected = rowsAffected
	}
	if lastInsertID, err := res.LastInsertId(); err == nil {
		out.LastInsertID = &lastInsertID
	}
	return out, nil
}

func (t *TaskContext) runtimeSqlQuery(query string, args ...any) (SqlQueryResult, error) {
	db, err := t.requireRuntimeSQL()
	if err != nil {
		return SqlQueryResult{}, err
	}
	rows, err := db.QueryContext(t.ctx, query, args...)
	if err != nil {
		return SqlQueryResult{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return SqlQueryResult{}, err
	}
	out := SqlQueryResult{
		Columns: append([]string(nil), cols...),
		Rows:    make([]map[string]any, 0),
	}
	values := make([]any, len(cols))
	scanArgs := make([]any, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return SqlQueryResult{}, err
		}
		item := make(map[string]any, len(cols))
		for i, name := range cols {
			item[name] = normalizeSQLValue(values[i])
		}
		out.Rows = append(out.Rows, item)
	}
	if err := rows.Err(); err != nil {
		return SqlQueryResult{}, err
	}
	return out, nil
}

// NLExec appends to the TaskContext's persistent conversation and returns raw text.
func (t *TaskContext) NLExec(prompt string, data map[string]any) (string, error) {
	t.logger.Debug("nl exec", "prompt_length", len(prompt))
	if err := t.ensureMainConversation(); err != nil {
		return "", err
	}
	instruction, err := nlExecInstruction(prompt, data)
	if err != nil {
		return "", err
	}
	r, err := t.newMainRunner(convrunner.RunnerConfig{})
	if err != nil {
		return "", err
	}
	if err := r.Run(t.ctx, instruction, convrunner.RunOptions{}); err != nil {
		return "", err
	}
	res := r.Wait()
	if res.Err != nil {
		return "", res.Err
	}
	return strings.TrimSpace(res.FinalText), nil
}

// NLExecTyped appends to the TaskContext's persistent conversation and decodes a JSON response into out.
func (t *TaskContext) NLExecTyped(prompt string, data map[string]any, out any) error {
	t.logger.Debug("nl exec typed", "prompt_length", len(prompt))
	if err := t.ensureMainConversation(); err != nil {
		return err
	}
	instruction, err := nlExecTypedInstruction(prompt, data)
	if err != nil {
		return err
	}
	schema, err := llm.JSONSchemaForOutputValue(out)
	if err != nil {
		return err
	}
	r, err := t.newMainRunner(convrunner.RunnerConfig{Tools: []tools.Tool{}})
	if err != nil {
		return err
	}
	if err := r.Run(t.ctx, instruction, convrunner.RunOptions{
		ResponseMimeType: llm.JSONSchemaFormat{SchemaObject: schema}.MimeType(),
		ResponseSchema:   schema,
	}); err != nil {
		return err
	}
	res := r.Wait()
	if res.Err != nil {
		return res.Err
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.FinalText)), out); err != nil {
		return fmt.Errorf("decode typed NLExec response: %w; raw response: %s", err, strings.TrimSpace(res.FinalText))
	}
	return nil
}

// NLExecTyped is a generic helper for typed NLExec return values.
func NLExecTyped[T any](tc *TaskContext, prompt string, data map[string]any) (T, error) {
	var out T
	err := tc.NLExecTyped(prompt, data, &out)
	return out, err
}

// NLCondition evaluates a natural-language condition in an isolated fork of the main conversation.
func (t *TaskContext) NLCondition(conditionID string, conditionPrompt string, data map[string]any) error {
	t.logger.Debug("nl condition", "condition_id", conditionID)
	if err := t.ensureMainConversation(); err != nil {
		return err
	}
	instruction, err := nlConditionInstruction(conditionID, conditionPrompt, data)
	if err != nil {
		return err
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"passed":      map[string]any{"type": "boolean"},
			"explanation": map[string]any{"type": "string"},
		},
		"required": []string{"passed", "explanation"},
	}
	res, err := t.runConditionBranch(instruction, schema)
	if err != nil {
		return err
	}

	parsed, err := parseConditionResultJSON(res)
	if err != nil {
		return fmt.Errorf("parse NL condition response: %w; raw response: %s", err, strings.TrimSpace(res))
	}
	if parsed.Passed {
		return nil
	}
	return &NLConditionError{
		ConditionID: conditionID,
		Prompt:      conditionPrompt,
		Context:     cloneContextMap(data),
		Explanation: strings.TrimSpace(parsed.Explanation),
	}
}

func (t *TaskContext) ensureMainConversation() error {
	db, err := t.ConversationDB()
	if err != nil {
		return err
	}
	ref := t.mainConversationRef()
	if _, err := db.GetRef(ref); err == nil {
		return t.ensureSystemPromptOnRef()
	} else if !errors.Is(err, convdb.ErrRefNotFound) {
		return err
	}
	if err := db.CreateRef(ref, convdb.RootHash); err != nil && !strings.Contains(err.Error(), "UNIQUE") {
		return err
	}
	return t.ensureSystemPromptOnRef()
}

func (t *TaskContext) resolveAPIKey() (string, error) {
	key, ok := modelpkg.ResolveCredential(t.apiKey, t.baseConfig.Model)
	if !ok {
		envVar := modelpkg.RequiredCredentialEnvVarForModel(t.baseConfig.Model)
		return "", fmt.Errorf(
			"%s is required for conversation-backed operations",
			envVar,
		)
	}
	t.apiKey = key
	return key, nil
}

func (t *TaskContext) conversationHistory() []llm.Message {
	db, err := t.ConversationDB()
	if err != nil {
		return []llm.Message{}
	}
	ref, err := db.GetRef(t.mainConversationRef())
	if err != nil {
		return []llm.Message{}
	}
	chain, err := db.LoadChain(ref.Hash)
	if err != nil {
		return []llm.Message{}
	}
	history := make([]llm.Message, 0, len(chain))
	for _, node := range chain {
		history = append(history, node.Message)
	}
	return history
}

func (t *TaskContext) mainConversationRef() string {
	if strings.TrimSpace(t.taskID) != "" {
		return "tasks/" + strings.TrimSpace(t.taskID) + "/main"
	}
	if strings.TrimSpace(t.namespace) != "" {
		return "tasks/" + strings.TrimSpace(t.namespace) + "/main"
	}
	return "tasks/default/main"
}

func (t *TaskContext) ensureSystemPromptOnRef() error {
	db, err := t.ConversationDB()
	if err != nil {
		return err
	}
	ref, err := db.GetRef(t.mainConversationRef())
	if err != nil {
		return err
	}
	chain, err := db.LoadChain(ref.Hash)
	if err != nil {
		return err
	}
	if len(chain) > 0 {
		return nil
	}
	systemPrompt := strings.TrimSpace(t.baseConfig.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = defaultConversationSystemPrompt
	}
	runnerID := fmt.Sprintf("taskctx-system-%d", time.Now().UTC().UnixNano())
	leasedRef, err := db.LeaseRef(t.mainConversationRef(), runnerID)
	if err != nil {
		return err
	}
	defer func() { _ = leasedRef.Close() }()
	chain, err = leasedRef.LoadChain()
	if err != nil {
		return err
	}
	if len(chain) > 0 {
		return nil
	}
	_, err = leasedRef.Append(convdb.Message{
		Role: llm.RoleSystem,
		Text: systemPrompt,
	})
	return err
}

func (t *TaskContext) appendToMainRef(msg llm.Message) error {
	db, err := t.ConversationDB()
	if err != nil {
		return err
	}
	runnerID := fmt.Sprintf("taskctx-append-%d", time.Now().UTC().UnixNano())
	leasedRef, err := db.LeaseRef(t.mainConversationRef(), runnerID)
	if err != nil {
		return err
	}
	defer func() { _ = leasedRef.Close() }()
	_, err = leasedRef.Append(convdb.Message(msg))
	return err
}

func (t *TaskContext) newMainRunner(extra convrunner.RunnerConfig) (*convrunner.ConversationRunner, error) {
	apiKey, err := t.resolveAPIKey()
	if err != nil {
		return nil, err
	}
	cfg := extra
	db, err := t.ConversationDB()
	if err != nil {
		return nil, err
	}
	if cfg.Ref == nil {
		runnerID := fmt.Sprintf("taskctx-runner-%d", time.Now().UTC().UnixNano())
		cfg.Ref, err = db.LeaseRef(t.mainConversationRef(), runnerID)
		if err != nil {
			return nil, err
		}
	}
	cfg.Client, err = t.newLLMClient(apiKey)
	if err != nil {
		_ = cfg.Ref.Close()
		return nil, err
	}
	cfg.Model = t.baseConfig.Model
	cfg.Temperature = t.baseConfig.Temperature
	cfg.SystemPrompt = t.baseConfig.SystemPrompt
	cfg.MaxTurns = t.baseConfig.MaxTurns
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = modelpkg.DefaultMaxTurns
	}
	if cfg.Tools == nil {
		toolset := append([]tools.Tool(nil), t.baseConfig.Tools...)
		if t.zoaFunctionTools != nil {
			zoaTools, err := t.zoaFunctionTools()
			if err != nil {
				_ = cfg.Ref.Close()
				return nil, fmt.Errorf("initialize zoafunction tools: %w", err)
			}
			toolset = append(toolset, zoaTools...)
		}
		cfg.Tools = toolset
	}
	return convrunner.NewRunner(cfg)
}

func (t *TaskContext) newLLMClient(credential string) (llm.Client, error) {
	switch modelpkg.InferProviderFromModel(t.baseConfig.Model) {
	case modelpkg.ProviderGemini:
		return llm.NewGeminiClient(credential), nil
	case modelpkg.ProviderAnthropic:
		return llm.NewAnthropicClientWithOAuthToken(credential), nil
	default:
		return nil, fmt.Errorf("unsupported model %q", t.baseConfig.Model)
	}
}

func (t *TaskContext) runConditionBranch(instruction string, schema map[string]any) (string, error) {
	apiKey, err := t.resolveAPIKey()
	if err != nil {
		return "", err
	}
	client, err := t.newLLMClient(apiKey)
	if err != nil {
		return "", err
	}
	db, err := t.ConversationDB()
	if err != nil {
		return "", err
	}
	ref, err := db.GetRef(t.mainConversationRef())
	if err != nil {
		return "", err
	}
	promptHash, err := db.Append(ref.Hash, convdb.Message{Role: llm.RoleUser, Text: instruction})
	if err != nil {
		return "", err
	}
	chain, err := db.LoadChain(promptHash)
	if err != nil {
		return "", err
	}
	resp, err := client.Complete(t.ctx, llm.CompletionRequest{
		Model:            t.baseConfig.Model,
		Messages:         taskChainMessages(chain),
		Temperature:      t.baseConfig.Temperature,
		ResponseMimeType: llm.JSONSchemaFormat{SchemaObject: schema}.MimeType(),
		ResponseSchema:   schema,
	})
	if err != nil {
		return "", err
	}
	_, err = db.Append(promptHash, convdb.Message{
		Role:      llm.RoleAssistant,
		Text:      resp.Text,
		Parts:     resp.Parts,
		ToolCalls: resp.ToolCalls,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func taskChainMessages(chain []convdb.Node) []llm.Message {
	out := make([]llm.Message, 0, len(chain))
	for _, node := range chain {
		out = append(out, node.Message)
	}
	return out
}

type NLConditionError struct {
	ConditionID string         `json:"condition_id"`
	Prompt      string         `json:"prompt"`
	Context     map[string]any `json:"context"`
	Explanation string         `json:"explanation"`
}

func (e *NLConditionError) Error() string {
	ctxJSON, _ := json.Marshal(e.Context)
	return fmt.Sprintf("nl condition failed [%s]: %s | prompt=%q | context=%s", e.ConditionID, e.Explanation, e.Prompt, string(ctxJSON))
}

func nlExecInstruction(prompt string, data map[string]any) (string, error) {
	if data == nil {
		return fmt.Sprintf(`
You are executing a ZoaFunction NLExec call.

Task:
%s

Return only the final answer text. Do not include markdown fences.
`, strings.TrimSpace(prompt)), nil
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal NLExec payload: %w", err)
	}
	return fmt.Sprintf(`
You are executing a ZoaFunction NLExec call.

Task:
%s

Context JSON:
%s

Return only the final answer text. Do not include markdown fences.
`, strings.TrimSpace(prompt), string(payload)), nil
}

func nlExecTypedInstruction(prompt string, data map[string]any) (string, error) {
	if data == nil {
		return fmt.Sprintf(`
You are executing a ZoaFunction typed NLExec call.

Task:
%s

Return ONLY valid JSON representing your final answer.
No markdown fences. No extra prose.
`, strings.TrimSpace(prompt)), nil
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal NLExec payload: %w", err)
	}
	return fmt.Sprintf(`
You are executing a ZoaFunction typed NLExec call.

Task:
%s

Context JSON:
%s

Return ONLY valid JSON representing your final answer.
No markdown fences. No extra prose.
`, strings.TrimSpace(prompt), string(payload)), nil
}

func nlConditionInstruction(conditionID string, conditionPrompt string, data map[string]any) (string, error) {
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal condition payload: %w", err)
	}
	return fmt.Sprintf(`
You are evaluating a ZoaFunction natural-language condition in isolation.

Condition ID:
%s

Condition to evaluate:
%s

Condition context JSON:
%s

Return ONLY a JSON object with this exact shape:
{"passed": <true|false>, "explanation": "short reason"}
`, conditionID, strings.TrimSpace(conditionPrompt), string(payload)), nil
}

type conditionJSON struct {
	Passed      bool   `json:"passed"`
	Explanation string `json:"explanation"`
}

func parseConditionResultJSON(text string) (conditionJSON, error) {
	var out conditionJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &out); err != nil {
		return conditionJSON{}, err
	}
	out.Explanation = strings.TrimSpace(out.Explanation)
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func cloneContextMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return map[string]any{"_unserializable": true}
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{"_unserializable": true}
	}
	return out
}
