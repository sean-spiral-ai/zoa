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
	"zoa/internal/gatewaychannel"
	"zoa/internal/gatewayclient"
	"zoa/internal/keys"
	"zoa/internal/llmtrace"
)

func runTUI(args []string) int {
	defaultCWD := "/"

	tuiFlags := flag.NewFlagSet("tui", flag.ContinueOnError)
	tuiFlags.SetOutput(os.Stderr)

	var (
		cwd             string
		sessionDir      string
		model           string
		maxTurns        int
		maxOutputTokens int
		temperature     float64
		timeoutSec      int
		pollMs          int
		llmtraceAddr    string
	)

	tuiFlags.StringVar(&cwd, "cwd", defaultCWD, "Workspace root for tools and task context")
	tuiFlags.StringVar(&sessionDir, "session-dir", gatewayclient.DefaultSessionDir, "Directory for gateway sqlite persistence")
	tuiFlags.StringVar(&model, "model", baselineagent.DefaultModel, "Model identifier")
	tuiFlags.IntVar(&maxTurns, "max-turns", baselineagent.DefaultMaxTurns, "Max model turns per prompt")
	tuiFlags.IntVar(&maxOutputTokens, "max-output-tokens", baselineagent.DefaultMaxOutputTokens, "Max model output tokens per completion")
	tuiFlags.Float64Var(&temperature, "temperature", baselineagent.DefaultTemperature, "Model temperature")
	tuiFlags.IntVar(&timeoutSec, "timeout", 300, "Per-prompt timeout (seconds)")
	tuiFlags.IntVar(&pollMs, "poll-ms", 400, "Outbox polling interval in milliseconds")
	tuiFlags.StringVar(&llmtraceAddr, "llmtrace-addr", ":3009", "LLM trace tree HTTP server address (empty to disable)")

	if err := tuiFlags.Parse(args); err != nil {
		return 2
	}
	if tuiFlags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional args: %s\n", strings.Join(tuiFlags.Args(), " "))
		return 2
	}
	if err := keys.LoadDotEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading .env: %v\n", err)
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

	var traceStore *llmtrace.Store
	if llmtraceAddr != "" {
		dbPath := filepath.Join(sessionDir, "llmtrace.db")
		var storeErr error
		traceStore, storeErr = llmtrace.NewStore(dbPath)
		if storeErr != nil {
			fmt.Fprintf(os.Stderr, "error creating llmtrace store: %v\n", storeErr)
			return 1
		}
		defer traceStore.Close()
		srv, _, srvErr := llmtrace.StartServer(llmtraceAddr, traceStore)
		if srvErr != nil {
			fmt.Fprintf(os.Stderr, "error starting llmtrace server: %v\n", srvErr)
			return 1
		}
		defer srv.Close()
		fmt.Fprintf(os.Stderr, "llmtrace server listening on %s\n", llmtraceAddr)
	}

	client, err := gatewayclient.NewLocalGatewayClient(gatewayclient.LocalConfig{
		Session:         gatewayclient.DefaultSession,
		SessionDir:      sessionDir,
		CWD:             cwd,
		Model:           model,
		MaxTurns:        maxTurns,
		MaxOutputTokens: maxOutputTokens,
		Temperature:     temperature,
		TimeoutSec:      timeoutSec,
		LLMTraceStore:   traceStore,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing gateway client: %v\n", err)
		return 1
	}
	defer func() {
		if err := client.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: close gateway client: %v\n", err)
		}
	}()

	fmt.Println("Gateway TUI started. Type messages to enqueue.")
	fmt.Println("Slash commands: /status /queue /log /tasks /outbox")
	fmt.Println("Local commands: /quit /exit")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollOutbox(ctx, client, time.Duration(pollMs)*time.Millisecond)

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

		recvRes, err := client.Enqueue(line, gatewaychannel.TUIURI)
		if err != nil {
			fmt.Fprintf(os.Stderr, "recv enqueue error: %v\n", err)
			continue
		}
		inboundIDText := "unknown"
		if recvRes.InboundID > 0 {
			inboundIDText = fmt.Sprintf("%d", recvRes.InboundID)
		}
		fmt.Printf("[recv inbound %s queued]\n", inboundIDText)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "input error: %v\n", err)
		return 1
	}
	return 0
}

func pollOutbox(ctx context.Context, client gatewayclient.GatewayClient, interval time.Duration) {
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
			messages, maxID, err := client.OutboxSince(lastID, 100)
			if err != nil {
				fmt.Fprintf(os.Stderr, "outbox poll error: %v\n", err)
				continue
			}
			for _, msg := range messages {
				if !isTUIOutboxChannel(msg.Channel) {
					continue
				}
				fmt.Printf("\n[%s #%d] %s\n", msg.Session, msg.ID, msg.Text)
			}
			lastID = maxID
		}
	}
}

func isTUIOutboxChannel(channelURI string) bool {
	channelURI = strings.TrimSpace(channelURI)
	if channelURI == "" {
		return true
	}
	target, err := gatewaychannel.Parse(channelURI)
	if err != nil {
		return false
	}
	return target.Transport == gatewaychannel.TransportTUI
}
