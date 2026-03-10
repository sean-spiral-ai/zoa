package gateway

import (
	"context"
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
	"zoa/hub"
	"zoa/internal/tracecontrol"
	"zoa/llm"
	modelpkg "zoa/model"
	"zoa/reliable"
	"zoa/runtime"
	tools "zoa/tools"
)

const (
	defaultGatewaySession   = "default"
	defaultChatSystemPrompt = `You are an assistant in a persistent chat session.
Use tools when they help. Be concise and factual.

You are running within the Zoa runtime, where you have access to ZoaFunctions: reusable workflows that can intersperse programmatic and natural-language execution in a composable way. Treat ZoaFunctions as your high-level toolbox.
You also have access to ZoaMixins: reusable context snippets that can be appended to the context window.

You can call ZoaFunctions via tools:
- search_zoafunctions: discover available ZoaFunctions by id/when_to_use.
- search_zoamixins: discover available Zoa context mixins by id/when_to_use.
- load_zoamixin: load a mixin so its content is appended to future NL context.
- call_zoafunction: start a ZoaFunction task and get a task_id.
- wait_zoafunction: wait for completion and read task output; if timeout is hit, you can wait again.
- kill_zoafunction: cancel a running ZoaFunction task by task_id.

If you feel missing context for a task, aggressively search for ZoaMixins and load relevant ones before proceeding.

Prefer ZoaFunctions for structured/reusable workflows and use regular coding tools for direct file/command work.`
	defaultPumpLimit       = 1
	defaultOutboxPollLimit = 100
	defaultChatTimeoutSec  = 3600
	maxOutboxPollLimit     = 500
	maxPumpLimit           = 200
	inboundMaxAttempts     = 3
	inboundLeaseDuration   = 2 * time.Minute
	defaultTraceControlURL = "http://127.0.0.1:3008"
)

var errInboundLeaseLost = errors.New("inbound lease lost")

func initFunction() *runtime.Function {
	return &runtime.Function{
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
		Exec: func(tc *runtime.TaskContext, _ map[string]any) (map[string]any, error) {
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

func recvFunction() *runtime.Function {
	return &runtime.Function{
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
				"timeout_sec": map[string]any{"type": "integer", "description": "Chat processing timeout in seconds. Default is 3600. Set to 0 to disable the per-message timeout (unlimited)."},
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
		Exec: func(tc *runtime.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)
			session, err := gatewaySessionInput(input)
			if err != nil {
				return nil, err
			}
			message, err := hub.StringInput(input, "message", true)
			if err != nil {
				return nil, err
			}
			channel, err := hub.StringInput(input, "channel", false)
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
			}, runtime.SpawnOptions{HideInLogByDefault: true}); err == nil {
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

func pumpFunction() *runtime.Function {
	return &runtime.Function{
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
		Exec: func(tc *runtime.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)
			session, err := gatewaySessionInput(input)
			if err != nil {
				return nil, err
			}
			limit, err := hub.IntInput(input, "limit", false)
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

func newInboundJobCompleter(state *state, tc *runtime.TaskContext, session string, lastOutboxID *int64) *reliable.JobCompleter[inboundPumpJob] {
	return &reliable.JobCompleter[inboundPumpJob]{
		MaxAttempts: inboundMaxAttempts,
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
			heartbeatStop := make(chan struct{})
			leaseLost := make(chan struct{}, 1)
			heartbeatInterval := inboundLeaseHeartbeatInterval(inboundLeaseDuration)
			go func() {
				if heartbeatInterval <= 0 {
					return
				}
				ticker := time.NewTicker(heartbeatInterval)
				defer ticker.Stop()
				for {
					select {
					case <-heartbeatStop:
						return
					case <-ticker.C:
						renewed, err := state.renewInboundLease(job.ID, job.Attempt, time.Now().UTC(), inboundLeaseDuration)
						if err != nil {
							slog.Warn("inbound lease heartbeat failed",
								"session", session,
								"inbound_id", job.ID,
								"attempt", job.Attempt,
								"error", err,
							)
							continue
						}
						if !renewed {
							select {
							case leaseLost <- struct{}{}:
							default:
							}
							return
						}
					}
				}
			}()

			reply, err := processInboundMessage(state, tc, job.Value.row)
			close(heartbeatStop)
			select {
			case <-leaseLost:
				return errInboundLeaseLost
			default:
			}
			if err != nil {
				return err
			}
			job.Value.reply = reply
			return nil
		},
		Complete: func(_ context.Context, job *reliable.ClaimedJob[inboundPumpJob], now time.Time) error {
			outboxID, claimed, err := state.completeInboundSuccess(session, job.Value.row.Channel, job.Value.reply, job.ID, job.Attempt, now)
			if err != nil {
				return err
			}
			if !claimed {
				return nil
			}
			if lastOutboxID != nil && outboxID > 0 {
				*lastOutboxID = outboxID
			}
			return nil
		},
		Retry: func(_ context.Context, job *reliable.ClaimedJob[inboundPumpJob], now time.Time, cause error, delay time.Duration) error {
			claimed, err := state.markInboundRetry(job.ID, job.Attempt, cause.Error(), now.Add(delay))
			if err != nil {
				return err
			}
			if !claimed {
				return nil
			}
			return nil
		},
		Fail: func(_ context.Context, job *reliable.ClaimedJob[inboundPumpJob], now time.Time, cause error) error {
			if errors.Is(cause, errInboundLeaseLost) {
				return nil
			}
			reply := fmt.Sprintf("Failed to process message %d: %v", job.ID, cause)
			outboxID, claimed, err := state.completeInboundFailure(session, job.Value.row.Channel, reply, job.ID, job.Attempt, now, cause.Error())
			if err != nil {
				return err
			}
			if !claimed {
				return nil
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

func outboxSinceFunction() *runtime.Function {
	return &runtime.Function{
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
		Exec: func(tc *runtime.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)
			session, err := gatewaySessionInput(input)
			if err != nil {
				return nil, err
			}

			lastID, err := hub.Int64Input(input, "last_id", false)
			if err != nil {
				return nil, err
			}
			limit, err := hub.IntInput(input, "limit", false)
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

func outboxMaxIDFunction() *runtime.Function {
	return &runtime.Function{
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
		Exec: func(tc *runtime.TaskContext, input map[string]any) (map[string]any, error) {
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

func processInboundMessage(state *state, tc *runtime.TaskContext, row *inboundRow) (string, error) {
	if row == nil {
		return "", fmt.Errorf("inbound row is nil")
	}
	if strings.HasPrefix(strings.TrimSpace(row.Text), "/") {
		reply, err := renderSlashResponse(state, tc, row.Session, row.Text)
		return reply, err
	}
	input := cloneMapAnyLocal(row.PumpInput)
	if input == nil {
		input = map[string]any{}
	}
	return processChatMessage(state, tc, input, row.Session, row.Text)
}

func processChatMessage(state *state, tc *runtime.TaskContext, input map[string]any, session string, message string) (string, error) {
	model, err := hub.StringInput(input, "model", false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		model = modelpkg.DefaultModel
	}
	model = strings.TrimSpace(model)
	if !modelpkg.IsSupportedModel(model) {
		return "", fmt.Errorf("unsupported model %q", model)
	}

	apiKey, ok := modelpkg.ResolveCredential("", model)
	if !ok {
		return "", fmt.Errorf("%s is required", modelpkg.RequiredCredentialEnvVarForModel(model))
	}

	cwd, err := hub.StringInput(input, "cwd", false)
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

	maxTurns, err := hub.IntInput(input, "max_turns", false)
	if err != nil {
		return "", err
	}
	if maxTurns <= 0 {
		maxTurns = modelpkg.DefaultMaxTurns
	}
	temperature, err := hub.FloatInput(input, "temperature", false)
	if err != nil {
		return "", err
	}
	if temperature == 0 {
		temperature = modelpkg.DefaultTemperature
	}

	timeoutSec := defaultChatTimeoutSec
	if _, hasTimeout := input["timeout_sec"]; hasTimeout {
		timeoutSec, err = hub.IntInput(input, "timeout_sec", false)
		if err != nil {
			return "", err
		}
		if timeoutSec < 0 {
			return "", fmt.Errorf("timeout_sec must be >= 0")
		}
	}

	codingTools, err := tools.NewCodingTools(absCWD)
	if err != nil {
		return "", fmt.Errorf("initialize builtin tools: %w", err)
	}
	zoaTools, err := tc.NewZoaFunctionTools()
	if err != nil {
		return "", fmt.Errorf("initialize zoafunction tools: %w", err)
	}
	codingTools = append(codingTools, zoaTools...)
	promptCtx := tc.Context()
	if promptCtx == nil {
		promptCtx = context.Background()
	}
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		promptCtx, cancel = context.WithTimeout(promptCtx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}

	client, err := gatewayLLMClient(model, apiKey)
	if err != nil {
		return "", err
	}
	conversationDB, err := tc.ConversationDB()
	if err != nil {
		return "", err
	}
	refName := "sessions/" + normalizeSession(session)
	if _, err := conversationDB.GetRef(refName); errors.Is(err, convdb.ErrRefNotFound) {
		if err := conversationDB.CreateRef(refName, convdb.RootHash); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	r, err := convrunner.NewRunner(convrunner.RunnerConfig{
		DB:           conversationDB,
		Ref:          refName,
		Client:       client,
		Model:        model,
		Tools:        codingTools,
		Temperature:  temperature,
		SystemPrompt: defaultChatSystemPrompt,
		MaxTurns:     maxTurns,
	})
	if err != nil {
		return "", err
	}
	if err := r.Run(promptCtx, message); err != nil {
		return "", err
	}
	res := r.Wait()
	if res.Err != nil {
		return "", res.Err
	}

	text := strings.TrimSpace(res.FinalText)
	if text == "" {
		text = "(no response)"
	}
	return text, nil
}

func gatewayLLMClient(model string, credential string) (llm.Client, error) {
	switch modelpkg.InferProviderFromModel(model) {
	case modelpkg.ProviderGemini:
		return llm.NewGeminiClient(credential), nil
	case modelpkg.ProviderAnthropic:
		return llm.NewAnthropicClientWithOAuthToken(credential), nil
	default:
		return nil, fmt.Errorf("unsupported model %q", model)
	}
}

func renderSlashResponse(state *state, tc *runtime.TaskContext, session string, text string) (string, error) {
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

func readTaskLogSummaries(tc *runtime.TaskContext, limit int, onlyRunning bool, includeHidden bool) ([]string, error) {
	if tc == nil {
		return nil, fmt.Errorf("task context is nil")
	}

	summaries, err := runtime.LogState(tc).Summaries(limit, onlyRunning, includeHidden)
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
	session, err := hub.StringInput(input, "session", false)
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

func inboundLeaseHeartbeatInterval(leaseDuration time.Duration) time.Duration {
	if leaseDuration <= 0 {
		return 0
	}
	interval := leaseDuration / 3
	if interval < 5*time.Second {
		return 5 * time.Second
	}
	if interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
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
	if strings.Contains(text, "deadline exceeded") {
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
	if cwd, err := hub.StringInput(input, "cwd", false); err != nil {
		return nil, err
	} else if strings.TrimSpace(cwd) != "" {
		out["cwd"] = strings.TrimSpace(cwd)
	}
	if model, err := hub.StringInput(input, "model", false); err != nil {
		return nil, err
	} else if strings.TrimSpace(model) != "" {
		out["model"] = strings.TrimSpace(model)
	}
	if maxTurns, err := hub.IntInput(input, "max_turns", false); err != nil {
		return nil, err
	} else if maxTurns > 0 {
		out["max_turns"] = maxTurns
	}
	if _, hasTimeout := input["timeout_sec"]; hasTimeout {
		timeoutSec, err := hub.IntInput(input, "timeout_sec", false)
		if err != nil {
			return nil, err
		}
		if timeoutSec < 0 {
			return nil, fmt.Errorf("timeout_sec must be >= 0")
		}
		out["timeout_sec"] = timeoutSec
	}
	if temperature, err := hub.FloatInput(input, "temperature", false); err != nil {
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
