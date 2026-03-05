package gatewayclient

import (
	"fmt"
	"path/filepath"
	"strings"

	baselineagent "zoa/baselineagent"
	"zoa/internal/llmtrace"
	diverseideation "zoa/lmflib/diverse_ideation"
	gatewaylmf "zoa/lmflib/gateway"
	"zoa/lmflib/intrinsic"
	mdtopdf "zoa/lmflib/md_to_pdf"
	lmfrt "zoa/lmfrt"
)

const (
	DefaultSession       = "default"
	DefaultSessionDir    = ".gateway/sessions/default"
	defaultTimeoutSec    = 300
	defaultOutboxPollLim = 100
)

type GatewayClient interface {
	Enqueue(message string, channelURI string) (EnqueueResult, error)
	OutboxSince(lastID int64, limit int) ([]OutboxMessage, int64, error)
	OutboxMaxID() (int64, error)
	Session() string
	Close() error
}

type LocalConfig struct {
	Session         string
	SessionDir      string
	CWD             string
	Model           string
	MaxTurns        int
	MaxOutputTokens int
	Temperature     float64
	TimeoutSec      int
	LLMTraceStore   *llmtrace.Store
}

type EnqueueResult struct {
	Accepted  bool
	Session   string
	InboundID int64
	Decision  string
	QueueLen  int64
}

type OutboxMessage struct {
	ID        int64
	Session   string
	Channel   string
	Text      string
	InReplyTo int64
	SentAt    string
}

type localGatewayClient struct {
	taskManager  *lmfrt.TaskManager
	session      string
	recvDefaults map[string]any
}

func NewLocalGatewayClient(cfg LocalConfig) (GatewayClient, error) {
	session := strings.TrimSpace(cfg.Session)
	if session == "" {
		session = DefaultSession
	}

	sessionDir := strings.TrimSpace(cfg.SessionDir)
	if sessionDir == "" {
		sessionDir = DefaultSessionDir
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = baselineagent.DefaultModel
	}
	if !baselineagent.IsSupportedModel(model) {
		return nil, fmt.Errorf(
			"unsupported model %q (supported: %s)",
			model,
			strings.Join(baselineagent.SupportedModelNames(), ", "),
		)
	}

	cwd := strings.TrimSpace(cfg.CWD)
	if cwd == "" {
		cwd = "/"
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = baselineagent.DefaultMaxTurns
	}
	maxOutputTokens := cfg.MaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = baselineagent.DefaultMaxOutputTokens
	}

	temperature := cfg.Temperature
	if temperature == 0 {
		temperature = baselineagent.DefaultTemperature
	}

	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSec
	}

	registry := lmfrt.NewRegistry()
	if err := intrinsic.RegisterMixins(registry); err != nil {
		return nil, fmt.Errorf("register intrinsic mixins: %w", err)
	}
	if err := gatewaylmf.RegisterFunctions(registry); err != nil {
		return nil, fmt.Errorf("register gateway functions: %w", err)
	}
	if err := diverseideation.RegisterFunctions(registry); err != nil {
		return nil, fmt.Errorf("register diverse_ideation functions: %w", err)
	}
	if err := mdtopdf.RegisterFunctions(registry); err != nil {
		return nil, fmt.Errorf("register md_to_pdf functions: %w", err)
	}

	taskManager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath:    filepath.Join(sessionDir, "state.db"),
		LLMTraceStore: cfg.LLMTraceStore,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize task manager: %w", err)
	}
	if err := taskManager.Init(); err != nil {
		_ = taskManager.Close()
		return nil, fmt.Errorf("run init functions: %w", err)
	}

	return &localGatewayClient{
		taskManager: taskManager,
		session:     session,
		recvDefaults: map[string]any{
			"session":           session,
			"cwd":               cwd,
			"model":             model,
			"max_turns":         maxTurns,
			"max_output_tokens": maxOutputTokens,
			"timeout_sec":       timeoutSec,
			"temperature":       temperature,
		},
	}, nil
}

func (c *localGatewayClient) Session() string {
	if c == nil {
		return ""
	}
	return c.session
}

func (c *localGatewayClient) Close() error {
	if c == nil || c.taskManager == nil {
		return nil
	}
	return c.taskManager.Close()
}

func (c *localGatewayClient) Enqueue(message string, channelURI string) (EnqueueResult, error) {
	if c == nil {
		return EnqueueResult{}, fmt.Errorf("gateway client is nil")
	}
	if strings.TrimSpace(message) == "" {
		return EnqueueResult{}, fmt.Errorf("message is required")
	}

	input := cloneMapAny(c.recvDefaults)
	input["message"] = message
	if channel := strings.TrimSpace(channelURI); channel != "" {
		input["channel"] = channel
	}

	snapshot, err := runAndWait(c.taskManager, "gateway.recv", input, lmfrt.SpawnOptions{})
	if err != nil {
		return EnqueueResult{}, err
	}

	accepted, _ := boolValue(snapshot.Output["accepted"])
	session, _ := snapshot.Output["session"].(string)
	inboundID, _ := int64Value(snapshot.Output["inbound_id"])
	decision, _ := snapshot.Output["decision"].(string)
	queueLen, _ := int64Value(snapshot.Output["queue_len"])

	return EnqueueResult{
		Accepted:  accepted,
		Session:   session,
		InboundID: inboundID,
		Decision:  decision,
		QueueLen:  queueLen,
	}, nil
}

func (c *localGatewayClient) OutboxSince(lastID int64, limit int) ([]OutboxMessage, int64, error) {
	if c == nil {
		return nil, lastID, fmt.Errorf("gateway client is nil")
	}
	if limit <= 0 {
		limit = defaultOutboxPollLim
	}

	snapshot, err := runAndWait(c.taskManager, "gateway.outbox_since", map[string]any{
		"session": c.session,
		"last_id": lastID,
		"limit":   limit,
	}, lmfrt.SpawnOptions{HideInLogByDefault: true})
	if err != nil {
		return nil, lastID, err
	}
	messages, maxID := outboxMessagesFromRunOutput(snapshot.Output, lastID)
	return messages, maxID, nil
}

// OutboxMaxID returns the current highest outbox ID for this session.
// Used to establish an in-memory watermark at startup so we only deliver
// new messages going forward.
func (c *localGatewayClient) OutboxMaxID() (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("gateway client is nil")
	}
	snapshot, err := runAndWait(c.taskManager, "gateway.outbox_max_id", map[string]any{
		"session": c.session,
	}, lmfrt.SpawnOptions{HideInLogByDefault: true})
	if err != nil {
		return 0, err
	}
	maxID, _ := int64Value(snapshot.Output["max_id"])
	return maxID, nil
}

func runAndWait(taskManager *lmfrt.TaskManager, functionID string, input map[string]any, opts lmfrt.SpawnOptions) (lmfrt.TaskSnapshot, error) {
	if taskManager == nil {
		return lmfrt.TaskSnapshot{}, fmt.Errorf("task manager is nil")
	}
	taskID, err := taskManager.Spawn(functionID, input, opts)
	if err != nil {
		return lmfrt.TaskSnapshot{}, err
	}
	snapshot, _, err := taskManager.Wait(taskID, 0)
	if err != nil {
		return lmfrt.TaskSnapshot{}, err
	}
	if snapshot.Status == lmfrt.TaskStatusFailed {
		if strings.TrimSpace(snapshot.Error) == "" {
			return lmfrt.TaskSnapshot{}, fmt.Errorf("task %s failed", taskID)
		}
		return lmfrt.TaskSnapshot{}, fmt.Errorf("%s", snapshot.Error)
	}
	if snapshot.Status != lmfrt.TaskStatusDone {
		return lmfrt.TaskSnapshot{}, fmt.Errorf("task %s ended in unexpected status %s", taskID, snapshot.Status)
	}
	return snapshot, nil
}

func outboxMessagesFromRunOutput(output map[string]any, fallbackLastID int64) ([]OutboxMessage, int64) {
	if output == nil {
		return nil, fallbackLastID
	}

	maxID := fallbackLastID
	if v, ok := int64Value(output["max_id"]); ok && v > maxID {
		maxID = v
	}

	rawMessages, ok := output["messages"]
	if !ok || rawMessages == nil {
		return nil, maxID
	}

	items := []OutboxMessage{}
	switch typed := rawMessages.(type) {
	case []map[string]any:
		for _, item := range typed {
			msg, ok := outboxMessageFromMap(item)
			if !ok {
				continue
			}
			items = append(items, msg)
			if msg.ID > maxID {
				maxID = msg.ID
			}
		}
	case []any:
		for _, row := range typed {
			item, ok := row.(map[string]any)
			if !ok {
				continue
			}
			msg, ok := outboxMessageFromMap(item)
			if !ok {
				continue
			}
			items = append(items, msg)
			if msg.ID > maxID {
				maxID = msg.ID
			}
		}
	}
	return items, maxID
}

func outboxMessageFromMap(item map[string]any) (OutboxMessage, bool) {
	id, ok := int64Value(item["id"])
	if !ok {
		return OutboxMessage{}, false
	}
	session, ok := item["session"].(string)
	if !ok {
		return OutboxMessage{}, false
	}
	text, ok := item["text"].(string)
	if !ok {
		return OutboxMessage{}, false
	}
	msg := OutboxMessage{
		ID:      id,
		Session: session,
		Text:    text,
	}
	if channel, ok := item["channel"].(string); ok {
		msg.Channel = channel
	}
	if replyID, ok := int64Value(item["in_reply_to"]); ok {
		msg.InReplyTo = replyID
	}
	if sentAt, ok := item["sent_at"].(string); ok {
		msg.SentAt = sentAt
	}
	return msg, true
}

func int64Value(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

func boolValue(v any) (bool, bool) {
	val, ok := v.(bool)
	return val, ok
}

func cloneMapAny(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
