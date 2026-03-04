package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	baselineagent "zoa/baselineagent"
	"zoa/internal/tracecontrol"
	"zoa/lmflib"
	lmfrt "zoa/lmfrt"
	"zoa/reliable"
)

const (
	defaultGatewaySession   = "default"
	defaultChatSystemPrompt = `You are an assistant in a persistent chat session.
Use tools when they help. Be concise and factual.

You are running within the LM Function Runtime, where you have access to LM Functions: reusable workflows that can intersperse programmatic and natural-language/agentic execution in a composable way. Treat LM Functions as your high-level toolbox.

You can call LM Functions via tools:
- search_lmfunctions: discover available LM Functions by id/when_to_use.
- call_lmfunction: start an LM Function task and get a task_id.
- wait_lmfunction: wait for completion and read task output.

Prefer LM Functions for structured/reusable workflows and use regular coding tools for direct file/command work.`
	defaultPumpLimit       = 1
	defaultOutboxPollLimit = 100
	maxOutboxPollLimit     = 500
	maxPumpLimit           = 200
	inboundLeaseDuration   = 2 * time.Minute
	defaultTraceControlURL = "http://127.0.0.1:3008"
)

func initFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.__init__",
		WhenToUse: "Run on startup to ensure gateway SQLite schema exists and is ready.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean"},
			},
			"required": []string{"ok"},
		},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			if err := newState(tc).init(); err != nil {
				return nil, err
			}
			if err := tc.RegisterPump(
				"gateway.default.pump",
				"gateway.pump",
				map[string]any{
					"session": defaultGatewaySession,
					"limit":   1,
				},
				time.Second,
			); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	}
}

func recvFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.recv",
		WhenToUse: "Queue a user message into a gateway session for ordered processing.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":     map[string]any{"type": "string", "description": "Gateway session identifier (defaults to default)"},
				"channel":     map[string]any{"type": "string", "description": "Gateway channel URI (for example gatewaychannel://tui or gatewaychannel://slack?channel_id=...)"},
				"message":     map[string]any{"type": "string", "description": "Raw user message text"},
				"cwd":         map[string]any{"type": "string"},
				"model":       map[string]any{"type": "string"},
				"max_turns":   map[string]any{"type": "integer"},
				"timeout_sec": map[string]any{"type": "integer"},
				"temperature": map[string]any{"type": "number"},
			},
			"required": []string{"message"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"accepted":   map[string]any{"type": "boolean"},
				"session":    map[string]any{"type": "string"},
				"inbound_id": map[string]any{"type": "integer"},
				"decision":   map[string]any{"type": "string"},
				"queue_len":  map[string]any{"type": "integer"},
			},
			"required": []string{"accepted", "session", "inbound_id", "decision", "queue_len"},
		},
		Exec: func(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)
			session, err := gatewaySessionInput(input)
			if err != nil {
				return nil, err
			}
			message, err := lmflib.StringInput(input, "message", true)
			if err != nil {
				return nil, err
			}
			channel, err := lmflib.StringInput(input, "channel", false)
			if err != nil {
				return nil, err
			}
			channel = strings.TrimSpace(channel)
			pumpInput, err := inboundPumpInputFromRecvInput(input)
			if err != nil {
				return nil, err
			}
			inboundID, err := state.insertInbound(session, channel, message, pumpInput, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			decision := "queued"
			if _, err := tc.Spawn("gateway.pump", map[string]any{
				"session": session,
				"limit":   1,
			}, lmfrt.SpawnOptions{HideInLogByDefault: true}); err == nil {
				decision = "queued_pump_triggered"
			} else {
				decision = "queued_pump_trigger_failed"
			}
			pending, err := state.pendingCount(session)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"accepted":   true,
				"session":    session,
				"inbound_id": inboundID,
				"decision":   decision,
				"queue_len":  pending,
			}, nil
		},
	}
}

func pumpFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.pump",
		WhenToUse: "Process queued inbound messages for a gateway session in strict id order.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string", "description": "Gateway session identifier (defaults to default)"},
				"limit":   map[string]any{"type": "integer", "description": "Maximum queued messages to process (default 1, max 200)"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":         map[string]any{"type": "string"},
				"processed":       map[string]any{"type": "integer"},
				"queue_len":       map[string]any{"type": "integer"},
				"last_inbound_id": map[string]any{"type": "integer"},
				"last_outbox_id":  map[string]any{"type": "integer"},
			},
			"required": []string{"session", "processed", "queue_len", "last_inbound_id", "last_outbox_id"},
		},
		Exec: func(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)
			session, err := gatewaySessionInput(input)
			if err != nil {
				return nil, err
			}
			limit, err := lmflib.IntInput(input, "limit", false)
			if err != nil {
				return nil, err
			}
			if limit <= 0 {
				limit = defaultPumpLimit
			}
			if limit > maxPumpLimit {
				limit = maxPumpLimit
			}

			processed := 0
			lastInboundID := int64(0)
			lastOutboxID := int64(0)
			completer := newInboundJobCompleter(state, tc, session, &lastOutboxID)
			for processed < limit {
				outcome, err := completer.CompleteOne(tc.Context())
				if err != nil {
					return nil, err
				}
				if !outcome.Claimed {
					break
				}
				processed++
				lastInboundID = outcome.JobID
			}

			pending, err := state.pendingCount(session)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"session":         session,
				"processed":       processed,
				"queue_len":       pending,
				"last_inbound_id": lastInboundID,
				"last_outbox_id":  lastOutboxID,
			}, nil
		},
	}
}

type inboundPumpJob struct {
	row   *inboundRow
	reply string
}

func newInboundJobCompleter(state *state, tc *lmfrt.TaskContext, session string, lastOutboxID *int64) *reliable.JobCompleter[inboundPumpJob] {
	return &reliable.JobCompleter[inboundPumpJob]{
		MaxAttempts: 3,
		Logger:      slog.Default().With("component", "inbound_pump", "session", session),
		ClaimDue: func(_ context.Context, now time.Time) (*reliable.ClaimedJob[inboundPumpJob], error) {
			row, err := state.claimDueInbound(session, now, inboundLeaseDuration)
			if err != nil {
				return nil, err
			}
			if row == nil {
				return nil, nil
			}
			return &reliable.ClaimedJob[inboundPumpJob]{
				ID:      row.ID,
				Attempt: row.Attempt,
				Value: inboundPumpJob{
					row: row,
				},
			}, nil
		},
		Handle: func(_ context.Context, job *reliable.ClaimedJob[inboundPumpJob]) error {
			reply, err := processInboundMessage(state, tc, job.Value.row)
			if err != nil {
				return err
			}
			job.Value.reply = reply
			return nil
		},
		Complete: func(_ context.Context, job *reliable.ClaimedJob[inboundPumpJob], now time.Time) error {
			outboxID, err := state.insertOutbox(session, job.Value.row.Channel, job.Value.reply, &job.ID, now)
			if err != nil {
				return err
			}
			if err := state.markInboundDone(job.ID, now); err != nil {
				return err
			}
			if lastOutboxID != nil && outboxID > 0 {
				*lastOutboxID = outboxID
			}
			return nil
		},
		Retry: func(_ context.Context, job *reliable.ClaimedJob[inboundPumpJob], now time.Time, cause error, delay time.Duration) error {
			return state.markInboundRetry(job.ID, cause.Error(), now.Add(delay))
		},
		Fail: func(_ context.Context, job *reliable.ClaimedJob[inboundPumpJob], now time.Time, cause error) error {
			reply := fmt.Sprintf("Failed to process message %d: %v", job.ID, cause)
			outboxID, err := state.insertOutbox(session, job.Value.row.Channel, reply, &job.ID, now)
			if err != nil {
				return err
			}
			if err := state.markInboundFailed(job.ID, cause.Error(), now); err != nil {
				return err
			}
			if lastOutboxID != nil && outboxID > 0 {
				*lastOutboxID = outboxID
			}
			return nil
		},
		ShouldRetry: isRetryableInboundError,
		Backoff:     inboundRetryBackoff,
	}
}

func outboxSinceFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.outbox_since",
		WhenToUse: "Poll outbound gateway messages since last seen id for a session.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string", "description": "Gateway session identifier (defaults to default)"},
				"last_id": map[string]any{"type": "integer", "description": "Return messages with id greater than this value"},
				"limit":   map[string]any{"type": "integer", "description": "Maximum number of messages to return (default 100, max 500)"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string"},
				"count":   map[string]any{"type": "integer"},
				"messages": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":          map[string]any{"type": "integer"},
							"session":     map[string]any{"type": "string"},
							"channel":     map[string]any{"type": "string"},
							"text":        map[string]any{"type": "string"},
							"in_reply_to": map[string]any{"type": "integer"},
							"sent_at":     map[string]any{"type": "string"},
						},
					},
				},
				"max_id": map[string]any{"type": "integer"},
			},
			"required": []string{"session", "count", "messages", "max_id"},
		},
		Exec: func(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)
			session, err := gatewaySessionInput(input)
			if err != nil {
				return nil, err
			}

			lastID, err := lmflib.Int64Input(input, "last_id", false)
			if err != nil {
				return nil, err
			}
			limit, err := lmflib.IntInput(input, "limit", false)
			if err != nil {
				return nil, err
			}
			if limit <= 0 {
				limit = defaultOutboxPollLimit
			}
			if limit > maxOutboxPollLimit {
				limit = maxOutboxPollLimit
			}

			rows, err := state.outboxSince(session, lastID, limit)
			if err != nil {
				return nil, err
			}

			items := make([]map[string]any, 0, len(rows))
			maxID := lastID
			for _, row := range rows {
				items = append(items, map[string]any{
					"id":          row.ID,
					"session":     row.Session,
					"channel":     row.Channel,
					"text":        row.Text,
					"in_reply_to": row.InReplyTo,
					"sent_at":     row.SentAt,
				})
				if row.ID > maxID {
					maxID = row.ID
				}
			}
			return map[string]any{
				"session":  session,
				"count":    len(items),
				"messages": items,
				"max_id":   maxID,
			}, nil
		},
	}
}

func outboxMaxIDFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.outbox_max_id",
		WhenToUse: "Return the current maximum outbox row ID for a session.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string", "description": "Gateway session identifier (defaults to default)"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"max_id": map[string]any{"type": "integer"},
			},
			"required": []string{"max_id"},
		},
		Exec: func(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)
			session, err := gatewaySessionInput(input)
			if err != nil {
				return nil, err
			}
			maxID, err := state.outboxMaxID(session)
			if err != nil {
				return nil, err
			}
			return map[string]any{"max_id": maxID}, nil
		},
	}
}

func processInboundMessage(state *state, tc *lmfrt.TaskContext, row *inboundRow) (string, error) {
	if row == nil {
		return "", fmt.Errorf("inbound row is nil")
	}
	if strings.HasPrefix(strings.TrimSpace(row.Text), "/") {
		return renderSlashResponse(state, tc, row.Session, row.Text)
	}
	input := cloneMapAnyLocal(row.PumpInput)
	if input == nil {
		input = map[string]any{}
	}
	return processChatMessage(state, tc, input, row.Session, row.Text)
}

func processChatMessage(state *state, tc *lmfrt.TaskContext, input map[string]any, session string, message string) (string, error) {
	model, err := lmflib.StringInput(input, "model", false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		model = baselineagent.DefaultModel
	}
	model = strings.TrimSpace(model)
	if !baselineagent.IsSupportedModel(model) {
		return "", fmt.Errorf("unsupported model %q", model)
	}

	apiKey, ok := baselineagent.ResolveCredential("", model)
	if !ok {
		return "", fmt.Errorf("%s is required", baselineagent.RequiredCredentialEnvVarForModel(model))
	}

	cwd, err := lmflib.StringInput(input, "cwd", false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cwd) == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve cwd: %w", err)
		}
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve absolute cwd: %w", err)
	}

	maxTurns, err := lmflib.IntInput(input, "max_turns", false)
	if err != nil {
		return "", err
	}
	if maxTurns <= 0 {
		maxTurns = baselineagent.DefaultMaxTurns
	}

	temperature, err := lmflib.FloatInput(input, "temperature", false)
	if err != nil {
		return "", err
	}
	if temperature == 0 {
		temperature = baselineagent.DefaultTemperature
	}

	timeoutSec, err := lmflib.IntInput(input, "timeout_sec", false)
	if err != nil {
		return "", err
	}
	if timeoutSec <= 0 {
		timeoutSec = 300
	}

	history, err := state.loadConversationHistory(session)
	if err != nil {
		return "", err
	}
	tools, err := baselineagent.NewBuiltinCodingTools(absCWD)
	if err != nil {
		return "", fmt.Errorf("initialize builtin tools: %w", err)
	}
	lmfTools, err := tc.NewLmFunctionTools()
	if err != nil {
		return "", fmt.Errorf("initialize lmfunction tools: %w", err)
	}
	tools = append(tools, lmfTools...)
	conv, err := baselineagent.NewConversation(apiKey, baselineagent.ConversationConfig{
		CWD:             absCWD,
		Model:           model,
		MaxTurns:        maxTurns,
		Timeout:         time.Duration(timeoutSec) * time.Second,
		Temperature:     temperature,
		SystemPrompt:    defaultChatSystemPrompt,
		Tools:           tools,
		InitialMessages: history,
		OnMessage: func(_ context.Context, msg baselineagent.ConversationMessage) error {
			return state.appendConversationMessage(session, msg, time.Now().UTC())
		},
	})
	if err != nil {
		return "", err
	}

	promptCtx := tc.Context()
	if promptCtx == nil {
		promptCtx = context.Background()
	}
	res, err := conv.Prompt(promptCtx, message)
	if err != nil {
		return "", err
	}

	text := strings.TrimSpace(res.FinalResponse)
	if text == "" {
		text = "(no response)"
	}
	return text, nil
}

func renderSlashResponse(state *state, tc *lmfrt.TaskContext, session string, text string) (string, error) {
	command := strings.Fields(strings.TrimSpace(text))
	if len(command) == 0 {
		return "", fmt.Errorf("invalid command")
	}
	session = normalizeSession(session)
	if session == "" {
		session = defaultGatewaySession
	}

	switch command[0] {
	case "/status":
		outboxCount, err := state.outboxCount(session)
		if err != nil {
			return "", err
		}
		pendingCount, err := state.pendingCount(session)
		if err != nil {
			return "", err
		}
		processingCount, err := state.processingCount(session)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"Session: %s\nPending: %d\nProcessing: %d\nOutbox: %d",
			session,
			pendingCount,
			processingCount,
			outboxCount,
		), nil
	case "/queue":
		pendingCount, err := state.pendingCount(session)
		if err != nil {
			return "", err
		}
		if pendingCount == 0 {
			return "Queue is empty.", nil
		}
		return fmt.Sprintf("Queue has %d pending message(s).", pendingCount), nil
	case "/outbox":
		rows, err := state.recentOutbox(session, 10)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 {
			return "Outbox is empty.", nil
		}
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		lines := []string{"Recent outbox messages:"}
		for _, row := range rows {
			lines = append(lines, fmt.Sprintf("- #%d [%s] %s", row.ID, row.Session, preview(row.Text, 80)))
		}
		return strings.Join(lines, "\n"), nil
	case "/log":
		items, err := readTaskLogSummaries(tc, 20, false, false)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "No task logs yet.", nil
		}
		lines := []string{"Recent tasks:"}
		for _, line := range items {
			lines = append(lines, "- "+line)
		}
		return strings.Join(lines, "\n"), nil
	case "/tasks":
		items, err := readTaskLogSummaries(tc, 0, true, true)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "No running tasks.", nil
		}
		lines := []string{"Running tasks:"}
		for _, line := range items {
			lines = append(lines, "- "+line)
		}
		return strings.Join(lines, "\n"), nil
	case "/start_trace":
		status, err := tracecontrol.StartTrace(resolveTraceControlURL())
		if err != nil {
			return fmt.Sprintf("Unable to start trace: %v", err), nil
		}
		return fmt.Sprintf("Runtime trace started.\nPath: %s\nStarted: %s\nWhen ready, run /end_trace.", status.TracePath, status.StartedAt.Format(time.RFC3339)), nil
	case "/end_trace":
		endResult, err := tracecontrol.DownloadTrace(resolveTraceControlURL())
		if err != nil {
			return fmt.Sprintf("Unable to end trace: %v", err), nil
		}
		outPath := filepath.Join(os.TempDir(), fmt.Sprintf("zoa-runtime-trace-%s.out", time.Now().UTC().Format("20060102T150405")))
		if err := os.WriteFile(outPath, endResult.GoTrace, 0o600); err != nil {
			return fmt.Sprintf("Trace ended but could not save output: %v", err), nil
		}
		semanticPath := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".semantic.json"
		semanticBytes, marshalErr := json.MarshalIndent(endResult.SemanticTrace, "", "  ")
		if marshalErr == nil {
			_ = os.WriteFile(semanticPath, semanticBytes, 0o600)
		}
		lines := []string{
			fmt.Sprintf("Runtime trace saved to %s (%d bytes).", outPath, len(endResult.GoTrace)),
			fmt.Sprintf("Semantic trace saved to %s (%d events).", semanticPath, len(endResult.SemanticTrace.Events)),
			fmt.Sprintf("Source trace path: %s", endResult.Status.TracePath),
			fmt.Sprintf("Convert/view: go tool trace %s", outPath),
		}
		return strings.Join(lines, "\n"), nil
	default:
		return "Unknown slash command. Available: /status /queue /log /tasks /outbox /start_trace /end_trace", nil
	}
}

func resolveTraceControlURL() string {
	raw := strings.TrimSpace(os.Getenv("ZOA_TRACE_CONTROL_URL"))
	if raw != "" {
		return strings.TrimRight(raw, "/")
	}
	return defaultTraceControlURL
}

func readTaskLogSummaries(tc *lmfrt.TaskContext, limit int, onlyRunning bool, includeHidden bool) ([]string, error) {
	if tc == nil {
		return nil, fmt.Errorf("task context is nil")
	}

	summaries, err := lmfrt.LogState(tc).Summaries(limit, onlyRunning, includeHidden)
	if err != nil {
		return nil, err
	}

	lines := make([]string, 0, len(summaries))
	for _, item := range summaries {
		line := fmt.Sprintf("%s %s [%s]", item.TaskID, item.FunctionID, item.Status)
		if strings.TrimSpace(item.Error) != "" {
			line += " error=" + preview(item.Error, 80)
		}
		if strings.TrimSpace(item.UpdatedAt) != "" {
			line += " updated_at=" + item.UpdatedAt
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func gatewaySessionInput(input map[string]any) (string, error) {
	session, err := lmflib.StringInput(input, "session", false)
	if err != nil {
		return "", err
	}
	session = normalizeSession(session)
	if session == "" {
		session = defaultGatewaySession
	}
	return session, nil
}

func normalizeSession(session string) string {
	return strings.TrimSpace(session)
}

func preview(text string, max int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if len(text) <= max {
		return text
	}
	if max <= 3 {
		return text[:max]
	}
	return text[:max-3] + "..."
}

func inboundRetryBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return time.Second
	case attempt == 2:
		return 2 * time.Second
	case attempt == 3:
		return 5 * time.Second
	case attempt == 4:
		return 10 * time.Second
	case attempt == 5:
		return 20 * time.Second
	default:
		return 30 * time.Second
	}
}

func isRetryableInboundError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	if strings.Contains(text, "overloaded") {
		return true
	}
	if strings.Contains(text, "rate limit") {
		return true
	}
	if strings.Contains(text, "timeout") {
		return true
	}
	if strings.Contains(text, "temporar") {
		return true
	}
	if strings.Contains(text, "connection reset") {
		return true
	}
	if strings.Contains(text, "connection refused") {
		return true
	}
	if strings.Contains(text, "internal server") {
		return true
	}
	if strings.Contains(text, "500") || strings.Contains(text, "502") || strings.Contains(text, "503") || strings.Contains(text, "504") || strings.Contains(text, "529") {
		return true
	}
	return false
}

func inboundPumpInputFromRecvInput(input map[string]any) (map[string]any, error) {
	out := map[string]any{}
	if input == nil {
		return out, nil
	}
	if cwd, err := lmflib.StringInput(input, "cwd", false); err != nil {
		return nil, err
	} else if strings.TrimSpace(cwd) != "" {
		out["cwd"] = strings.TrimSpace(cwd)
	}
	if model, err := lmflib.StringInput(input, "model", false); err != nil {
		return nil, err
	} else if strings.TrimSpace(model) != "" {
		out["model"] = strings.TrimSpace(model)
	}
	if maxTurns, err := lmflib.IntInput(input, "max_turns", false); err != nil {
		return nil, err
	} else if maxTurns > 0 {
		out["max_turns"] = maxTurns
	}
	if timeoutSec, err := lmflib.IntInput(input, "timeout_sec", false); err != nil {
		return nil, err
	} else if timeoutSec > 0 {
		out["timeout_sec"] = timeoutSec
	}
	if temperature, err := lmflib.FloatInput(input, "temperature", false); err != nil {
		return nil, err
	} else if temperature != 0 {
		out["temperature"] = temperature
	}
	return out, nil
}

func cloneMapAnyLocal(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
