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
	"zoa/gateway"
	gatewaylmf "zoa/lmflib/gateway"
	"zoa/lmflib/intrinsic"
	lmfrt "zoa/lmfrt"
)

func main() {
	defaultCWD := "/"

	var (
		cwd         string
		sessionDir  string
		model       string
		maxTurns    int
		temperature float64
		timeoutSec  int
		pollMs      int
	)

	flag.StringVar(&cwd, "cwd", defaultCWD, "Workspace root for tools and task context")
	flag.StringVar(&sessionDir, "session-dir", ".gateway/sessions/default", "Directory for gateway snapshot/task-log persistence")
	flag.StringVar(&model, "model", baselineagent.DefaultModel, "Model identifier")
	flag.IntVar(&maxTurns, "max-turns", baselineagent.DefaultMaxTurns, "Max model turns per prompt")
	flag.Float64Var(&temperature, "temperature", baselineagent.DefaultTemperature, "Model temperature")
	flag.IntVar(&timeoutSec, "timeout", 300, "Per-prompt timeout (seconds)")
	flag.IntVar(&pollMs, "poll-ms", 400, "Outbox polling interval in milliseconds")
	flag.Parse()

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
		os.Exit(1)
	}

	apiKey, ok := baselineagent.ResolveCredential("", model)
	if !ok {
		apiKey = ""
		envVar := baselineagent.RequiredCredentialEnvVarForModel(model)
		fmt.Fprintf(
			os.Stderr,
			"warning: %s is not set; non-slash chat messages will fail until configured\n",
			envVar,
		)
	}

	registry := intrinsic.NewRegistry()
	taskManager, err := lmfrt.NewTaskManager(registry, lmfrt.TaskManagerOptions{
		TaskLogDir: filepath.Join(sessionDir, "tasks"),
		SQLitePath: filepath.Join(sessionDir, "state.db"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing task manager: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := taskManager.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: close task manager: %v\n", err)
		}
	}()

	lmfTools, err := lmfrt.NewLMFunctionTools(registry, taskManager)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing LMFunction tools: %v\n", err)
		os.Exit(1)
	}
	builtinTools, err := baselineagent.NewBuiltinCodingTools(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing builtin tools: %v\n", err)
		os.Exit(1)
	}

	allTools := append([]baselineagent.Tool{}, builtinTools...)
	allTools = append(allTools, lmfTools...)

	service, err := gateway.NewService(gateway.ServiceConfig{
		SessionDir:  sessionDir,
		TaskLogDir:  filepath.Join(sessionDir, "tasks"),
		APIKey:      apiKey,
		CWD:         cwd,
		Model:       model,
		MaxTurns:    maxTurns,
		Timeout:     time.Duration(timeoutSec) * time.Second,
		Temperature: temperature,
		Tools:       allTools,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing gateway service: %v\n", err)
		os.Exit(1)
	}

	if err := gatewaylmf.RegisterFunctions(registry, service); err != nil {
		fmt.Fprintf(os.Stderr, "error registering gateway functions: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Gateway TUI started. Type messages to enqueue.")
	fmt.Println("Slash commands: /status /queue /log /tasks /outbox")
	fmt.Println("Local commands: /quit /exit")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollOutbox(ctx, service, time.Duration(pollMs)*time.Millisecond)

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

		taskID, err := taskManager.Spawn("gateway.recv", map[string]any{
			"channel":     "tui",
			"message":     line,
			"cwd":         cwd,
			"model":       model,
			"max_turns":   maxTurns,
			"timeout_sec": timeoutSec,
			"temperature": temperature,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "recv spawn error: %v\n", err)
			continue
		}
		fmt.Printf("[recv task %s queued]\n", taskID)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "input error: %v\n", err)
	}
}

func pollOutbox(ctx context.Context, service *gateway.Service, interval time.Duration) {
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
			messages := service.OutboxSince(lastID)
			for _, msg := range messages {
				fmt.Printf("\n[%s #%d] %s\n", msg.Channel, msg.ID, msg.Text)
				if msg.ID > lastID {
					lastID = msg.ID
				}
			}
		}
	}
}
