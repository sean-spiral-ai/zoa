package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	baselineagent "zoa/baselineagent"
	"zoa/lmflib"
	lmfrt "zoa/lmfrt"
)

const defaultChatSystemPrompt = `You are an assistant in a persistent chat session.
Use tools when they help. Be concise and factual.`

func initFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.__init__",
		WhenToUse: "Run on startup to ensure gateway SQLite schema exists and is ready.",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Exec: func(tc *lmfrt.TaskContext, _ map[string]any) (map[string]any, error) {
			if err := newState(tc).init(); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		},
	}
}

func recvFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.recv",
		WhenToUse: "Use at channel ingress to persist and process a user message into gateway outbox.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel":      map[string]any{"type": "string", "description": "Source channel identifier (defaults to tui)"},
				"message":      map[string]any{"type": "string", "description": "Raw user message text"},
				"task_log_dir": map[string]any{"type": "string", "description": "Task log directory for slash command summaries"},
				"cwd":          map[string]any{"type": "string"},
				"model":        map[string]any{"type": "string"},
				"max_turns":    map[string]any{"type": "integer"},
				"timeout_sec":  map[string]any{"type": "integer"},
				"temperature":  map[string]any{"type": "number"},
			},
			"required": []string{"message"},
		},
		Exec: func(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)

			channel, err := lmflib.StringInput(input, "channel", false)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(channel) == "" {
				channel = "tui"
			}
			message, err := lmflib.StringInput(input, "message", true)
			if err != nil {
				return nil, err
			}

			now := time.Now().UTC()
			inboundID, err := state.insertInbound(channel, message, now)
			if err != nil {
				return nil, err
			}

			taskLogDir, err := lmflib.StringInput(input, "task_log_dir", false)
			if err != nil {
				return nil, err
			}

			if strings.HasPrefix(strings.TrimSpace(message), "/") {
				reply, err := renderSlashResponse(state, taskLogDir, message)
				if err != nil {
					return nil, err
				}
				outID, err := state.insertOutbox(channel, reply, nil, now)
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"accepted":   true,
					"message_id": outID,
					"decision":   "slash_handled",
					"queue_len":  0,
				}, nil
			}

			reply, err := processChatMessage(state, input, message)
			if err != nil {
				reply = fmt.Sprintf("Failed to process message %d: %v", inboundID, err)
			}
			if _, err := state.insertOutbox(channel, reply, &inboundID, time.Now().UTC()); err != nil {
				return nil, err
			}
			return map[string]any{
				"accepted":   true,
				"message_id": inboundID,
				"decision":   "processed",
				"queue_len":  0,
			}, nil
		},
	}
}

func outboxSinceFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.outbox_since",
		WhenToUse: "Use to poll outbound gateway messages since the last seen message id for a channel.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel": map[string]any{"type": "string", "description": "Channel identifier filter (defaults to tui)"},
				"last_id": map[string]any{"type": "integer", "description": "Return messages with id greater than this value"},
				"limit":   map[string]any{"type": "integer", "description": "Maximum number of messages to return (default 100, max 500)"},
			},
		},
		Exec: func(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			state := newState(tc)

			channel, err := lmflib.StringInput(input, "channel", false)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(channel) == "" {
				channel = "tui"
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
				limit = 100
			}
			if limit > 500 {
				limit = 500
			}

			rows, err := state.outboxSince(channel, lastID, limit)
			if err != nil {
				return nil, err
			}

			items := make([]map[string]any, 0, len(rows))
			maxID := lastID
			for _, row := range rows {
				items = append(items, map[string]any{
					"id":          row.ID,
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
				"channel":  channel,
				"count":    len(items),
				"messages": items,
				"max_id":   maxID,
			}, nil
		},
	}
}

func processChatMessage(state *state, input map[string]any, message string) (string, error) {
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

	history, err := state.loadConversationHistory()
	if err != nil {
		return "", err
	}
	tools, err := baselineagent.NewBuiltinCodingTools(absCWD)
	if err != nil {
		return "", fmt.Errorf("initialize builtin tools: %w", err)
	}
	conv, err := baselineagent.NewConversation(apiKey, baselineagent.ConversationConfig{
		CWD:             absCWD,
		Model:           model,
		MaxTurns:        maxTurns,
		Timeout:         time.Duration(timeoutSec) * time.Second,
		Temperature:     temperature,
		SystemPrompt:    defaultChatSystemPrompt,
		Tools:           tools,
		InitialMessages: history,
	})
	if err != nil {
		return "", err
	}

	res, err := conv.Prompt(context.Background(), message)
	if err != nil {
		return "", err
	}
	if err := state.saveConversationHistory(conv.History(), time.Now().UTC()); err != nil {
		return "", err
	}
	text := strings.TrimSpace(res.FinalResponse)
	if text == "" {
		text = "(no response)"
	}
	return text, nil
}

func renderSlashResponse(state *state, taskLogDir string, text string) (string, error) {
	command := strings.Fields(strings.TrimSpace(text))
	if len(command) == 0 {
		return "", fmt.Errorf("invalid command")
	}
	switch command[0] {
	case "/status":
		outboxCount, err := state.outboxCount()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"Session: default\nState: idle\nActive: none\nQueue: 0\nOutbox: %d",
			outboxCount,
		), nil
	case "/queue":
		return "Queue is empty.", nil
	case "/outbox":
		rows, err := state.recentOutbox(10)
		if err != nil {
			return "", err
		}
		if len(rows) == 0 {
			return "Outbox is empty.", nil
		}
		// Oldest-to-newest order in the rendered list.
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		lines := []string{"Recent outbox messages:"}
		for _, row := range rows {
			lines = append(lines, fmt.Sprintf("- #%d [%s] %s", row.ID, row.Channel, preview(row.Text, 80)))
		}
		return strings.Join(lines, "\n"), nil
	case "/log":
		items, err := readTaskLogSummaries(taskLogDir, 20, false)
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
		items, err := readTaskLogSummaries(taskLogDir, 0, true)
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
	default:
		return "Unknown slash command. Available: /status /queue /log /tasks /outbox", nil
	}
}

func readTaskLogSummaries(taskDir string, limit int, onlyRunning bool) ([]string, error) {
	taskDir = strings.TrimSpace(taskDir)
	if taskDir == "" {
		return []string{}, nil
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read task log dir: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "task-") && strings.HasSuffix(name, ".json") {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	if limit > 0 && len(files) > limit {
		files = files[len(files)-limit:]
	}
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}

	lines := make([]string, 0, len(files))
	for _, name := range files {
		path := filepath.Join(taskDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed struct {
			TaskID     string `json:"task_id"`
			FunctionID string `json:"function_id"`
			Status     string `json:"status"`
			Error      string `json:"error,omitempty"`
			UpdatedAt  string `json:"updated_at,omitempty"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		if onlyRunning && parsed.Status != "running" {
			continue
		}
		line := fmt.Sprintf("%s %s [%s]", parsed.TaskID, parsed.FunctionID, parsed.Status)
		if parsed.Error != "" {
			line += " error=" + preview(parsed.Error, 80)
		}
		if parsed.UpdatedAt != "" {
			line += " updated_at=" + parsed.UpdatedAt
		}
		lines = append(lines, line)
	}
	return lines, nil
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
