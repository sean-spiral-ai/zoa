package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	baselineagent "zoa/baselineagent"
	diverseideation "zoa/lmflib/diverse_ideation"
	gatewaylmf "zoa/lmflib/gateway"
	"zoa/lmflib/intrinsic"
	lmfrt "zoa/lmfrt"
)

const defaultGatewaySession = "default"

func runTUI(args []string) int {
	defaultCWD := "/"

	tuiFlags := flag.NewFlagSet("tui", flag.ContinueOnError)
	tuiFlags.SetOutput(os.Stderr)

	var (
		cwd         string
		sessionDir  string
		model       string
		maxTurns    int
		temperature float64
		timeoutSec  int
		pollMs      int
	)

	tuiFlags.StringVar(&cwd, "cwd", defaultCWD, "Workspace root for tools and task context")
	tuiFlags.StringVar(&sessionDir, "session-dir", ".gateway/sessions/default", "Directory for gateway sqlite persistence")
	tuiFlags.StringVar(&model, "model", baselineagent.DefaultModel, "Model identifier")
	tuiFlags.IntVar(&maxTurns, "max-turns", baselineagent.DefaultMaxTurns, "Max model turns per prompt")
	tuiFlags.Float64Var(&temperature, "temperature", baselineagent.DefaultTemperature, "Model temperature")
	tuiFlags.IntVar(&timeoutSec, "timeout", 300, "Per-prompt timeout (seconds)")
	tuiFlags.IntVar(&pollMs, "poll-ms", 400, "Outbox polling interval in milliseconds")

	if err := tuiFlags.Parse(args); err != nil {
		return 2
	}
	if tuiFlags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional args: %s\n", strings.Join(tuiFlags.Args(), " "))
		return 2
	}

	if strings.TrimSpace(model) == "" {
		model = baselineagent.DefaultModel
	}
	model = strings.TrimSpace(model)
	if !baselineagent.IsSupportedModel(model) {
		fmt.Fprintf(
			os.Stderr,
			"error: unsupported model %q (supported: %s)\n",
			model,
			strings.Join(baselineagent.SupportedModelNames(), ", "),
		)
		return 1
	}

	_, ok := baselineagent.ResolveCredential("", model)
	if !ok {
		envVar := baselineagent.RequiredCredentialEnvVarForModel(model)
		fmt.Fprintf(
			os.Stderr,
			"warning: %s is not set; non-slash chat messages will fail until configured\n",
			envVar,
		)
	}

	registry := lmfrt.NewRegistry()
	if err := intrinsic.RegisterFunctions(registry); err != nil {
		fmt.Fprintf(os.Stderr, "error registering intrinsic functions: %v\n", err)
		return 1
	}
	taskManager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		SQLitePath: filepath.Join(sessionDir, "state.db"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing task manager: %v\n", err)
		return 1
	}
	defer func() {
		if err := taskManager.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: close task manager: %v\n", err)
		}
	}()

	if err := gatewaylmf.RegisterFunctions(registry); err != nil {
		fmt.Fprintf(os.Stderr, "error registering gateway functions: %v\n", err)
		return 1
	}
	if err := diverseideation.RegisterFunctions(registry); err != nil {
		fmt.Fprintf(os.Stderr, "error registering diverse_ideation functions: %v\n", err)
		return 1
	}
	if err := taskManager.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "error running init functions: %v\n", err)
		return 1
	}

	fmt.Println("Gateway TUI started. Type messages to enqueue.")
	fmt.Println("Slash commands: /status /queue /log /tasks /outbox")
	fmt.Println("Local commands: /quit /exit")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollOutbox(ctx, taskManager, time.Duration(pollMs)*time.Millisecond)

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			break
		}

		recvSnapshot, err := runAndWait(taskManager, "gateway.recv", map[string]any{
			"session":     defaultGatewaySession,
			"message":     line,
			"cwd":         cwd,
			"model":       model,
			"max_turns":   maxTurns,
			"timeout_sec": timeoutSec,
			"temperature": temperature,
		}, lmfrt.SpawnOptions{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "recv enqueue error: %v\n", err)
			continue
		}
		inboundIDText := "unknown"
		if inboundID, ok := int64Value(recvSnapshot.Output["inbound_id"]); ok && inboundID > 0 {
			inboundIDText = fmt.Sprintf("%d", inboundID)
		}
		fmt.Printf("[recv inbound %s queued]\n", inboundIDText)

	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "input error: %v\n", err)
		return 1
	}
	return 0
}

func pollOutbox(ctx context.Context, taskManager *lmfrt.TaskManager, interval time.Duration) {
	if interval <= 0 {
		interval = 400 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastID int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot, err := runAndWait(taskManager, "gateway.outbox_since", map[string]any{
				"session": defaultGatewaySession,
				"last_id": lastID,
				"limit":   100,
			}, lmfrt.SpawnOptions{HideInLogByDefault: true})
			if err != nil {
				fmt.Fprintf(os.Stderr, "outbox poll error: %v\n", err)
				continue
			}
			messages, maxID := outboxMessagesFromRunOutput(snapshot.Output, lastID)
			for _, msg := range messages {
				fmt.Printf("\n[%s #%d] %s\n", msg.Session, msg.ID, msg.Text)
			}
			lastID = maxID
		}
	}
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

type outboxMessage struct {
	ID        int64
	Session   string
	Text      string
	InReplyTo int64
}

func outboxMessagesFromRunOutput(output map[string]any, fallbackLastID int64) ([]outboxMessage, int64) {
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

	items := []outboxMessage{}
	switch typed := rawMessages.(type) {
	case []map[string]any:
		for _, item := range typed {
			msg, ok := outboundMessageFromMap(item)
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
			msg, ok := outboundMessageFromMap(item)
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

func outboundMessageFromMap(item map[string]any) (outboxMessage, bool) {
	id, ok := int64Value(item["id"])
	if !ok {
		return outboxMessage{}, false
	}
	session, ok := item["session"].(string)
	if !ok {
		return outboxMessage{}, false
	}
	text, ok := item["text"].(string)
	if !ok {
		return outboxMessage{}, false
	}
	msg := outboxMessage{
		ID:      id,
		Session: session,
		Text:    text,
	}
	if replyID, ok := int64Value(item["in_reply_to"]); ok {
		msg.InReplyTo = replyID
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
