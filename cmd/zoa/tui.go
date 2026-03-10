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

	"zoa/conversation"
	convdb "zoa/conversation/db"
	"zoa/internal/gatewaychannel"
	"zoa/internal/gatewayclient"
	"zoa/internal/keys"
	modelpkg "zoa/internal/agentloop/model"
)

func runTUI(args []string) int {
	defaultCWD := "/"

	tuiFlags := flag.NewFlagSet("tui", flag.ContinueOnError)
	tuiFlags.SetOutput(os.Stderr)

	var (
		cwd              string
		sessionDir       string
		model            string
		maxTurns         int
		temperature      float64
		timeoutSec       int
		pollMs           int
		conversationAddr string
	)

	tuiFlags.StringVar(&cwd, "cwd", defaultCWD, "Workspace root for tools and task context")
	tuiFlags.StringVar(&sessionDir, "session-dir", gatewayclient.DefaultSessionDir, "Directory for gateway sqlite persistence")
	tuiFlags.StringVar(&model, "model", modelpkg.DefaultModel, "Model identifier")
	tuiFlags.IntVar(&maxTurns, "max-turns", modelpkg.DefaultMaxTurns, "Max model turns per prompt")
	tuiFlags.Float64Var(&temperature, "temperature", modelpkg.DefaultTemperature, "Model temperature")
	tuiFlags.IntVar(&timeoutSec, "timeout", 3600, "Per-prompt timeout in seconds (0 disables timeout)")
	tuiFlags.IntVar(&pollMs, "poll-ms", 400, "Outbox polling interval in milliseconds")
	tuiFlags.StringVar(&conversationAddr, "conversation-addr", ":3009", "Conversation tree HTTP server address (empty to disable)")

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
		model = modelpkg.DefaultModel
	}
	model = strings.TrimSpace(model)
	if !modelpkg.IsSupportedModel(model) {
		fmt.Fprintf(
			os.Stderr,
			"error: unsupported model %q (supported: %s)\n",
			model,
			strings.Join(modelpkg.SupportedModelNames(), ", "),
		)
		return 1
	}

	_, ok := modelpkg.ResolveCredential("", model)
	if !ok {
		envVar := modelpkg.RequiredCredentialEnvVarForModel(model)
		fmt.Fprintf(
			os.Stderr,
			"warning: %s is not set; non-slash chat messages will fail until configured\n",
			envVar,
		)
	}

	var conversationDB *convdb.DB
	if conversationAddr != "" {
		dbPath := filepath.Join(sessionDir, "conversation.db")
		var storeErr error
		conversationDB, storeErr = convdb.Open(dbPath)
		if storeErr != nil {
			fmt.Fprintf(os.Stderr, "error creating conversation db: %v\n", storeErr)
			return 1
		}
		defer conversationDB.Close()
		srv, _, srvErr := conversation.StartServer(conversationAddr, conversationDB)
		if srvErr != nil {
			fmt.Fprintf(os.Stderr, "error starting conversation server: %v\n", srvErr)
			return 1
		}
		defer srv.Close()
		fmt.Fprintf(os.Stderr, "conversation server listening on %s\n", conversationAddr)
	}

	client, err := gatewayclient.NewLocalGatewayClient(gatewayclient.LocalConfig{
		Session:     gatewayclient.DefaultSession,
		SessionDir:  sessionDir,
		CWD:         cwd,
		Model:       model,
		MaxTurns:    maxTurns,
		Temperature: temperature,
		TimeoutSec:  timeoutSec,
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
