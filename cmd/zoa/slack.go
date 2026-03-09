package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"zoa/internal/gatewayclient"
	"zoa/internal/keys"
	"zoa/internal/llmtrace"
	slackbridge "zoa/internal/slack"
	modelpkg "zoa/model"
)

func runSlack(args []string) int {
	defaultCWD := "/"

	slackFlags := flag.NewFlagSet("slack", flag.ContinueOnError)
	slackFlags.SetOutput(os.Stderr)

	var (
		cwd           string
		sessionDir    string
		model         string
		maxTurns      int
		temperature   float64
		timeoutSec    int
		pollMs        int
		traceHTTPAddr string
		llmtraceAddr  string

		appTokenFlag      string
		botTokenFlag      string
		logLevel          string
		debugLogComponent string
	)

	slackFlags.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	slackFlags.StringVar(&debugLogComponent, "debug-log-component", "", "When set, only DEBUG logs with this component are emitted")
	slackFlags.StringVar(&cwd, "cwd", defaultCWD, "Workspace root for tools and task context")
	slackFlags.StringVar(&sessionDir, "session-dir", gatewayclient.DefaultSessionDir, "Directory for gateway sqlite persistence")
	slackFlags.StringVar(&model, "model", modelpkg.DefaultModel, "Model identifier")
	slackFlags.IntVar(&maxTurns, "max-turns", modelpkg.DefaultMaxTurns, "Max model turns per prompt")
	slackFlags.Float64Var(&temperature, "temperature", modelpkg.DefaultTemperature, "Model temperature")
	slackFlags.IntVar(&timeoutSec, "timeout", 3600, "Per-prompt timeout in seconds (0 disables timeout)")
	slackFlags.IntVar(&pollMs, "poll-ms", 400, "Outbox polling interval in milliseconds")
	slackFlags.StringVar(&traceHTTPAddr, "trace-http-addr", "127.0.0.1:3008", "runtime trace control HTTP listen address (empty to disable)")
	slackFlags.StringVar(&llmtraceAddr, "llmtrace-addr", ":3009", "LLM trace tree HTTP server address (empty to disable)")

	slackFlags.StringVar(&appTokenFlag, "slack-app-token", "", "Slack app token (xapp-..., default: SLACK_APP_TOKEN)")
	slackFlags.StringVar(&botTokenFlag, "slack-bot-token", "", "Slack bot token (xoxb-..., default: SLACK_BOT_TOKEN)")

	if err := slackFlags.Parse(args); err != nil {
		return 2
	}
	if slackFlags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional args: %s\n", strings.Join(slackFlags.Args(), " "))
		return 2
	}
	var slogLevel slog.Level
	if err := slogLevel.UnmarshalText([]byte(logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid log level %q (use debug, info, warn, error)\n", logLevel)
		return 2
	}
	handler := slog.Handler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel,
	}))
	if strings.TrimSpace(debugLogComponent) != "" {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
		handler = newDebugComponentFilterHandler(handler, slogLevel, strings.TrimSpace(debugLogComponent))
	}
	slog.SetDefault(slog.New(handler))
	traceServer, traceBaseURL, err := startRuntimeTraceControlServer(traceHTTPAddr, slog.Default())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: start runtime trace control server: %v\n", err)
	} else if traceServer != nil {
		if setErr := os.Setenv("ZOA_TRACE_CONTROL_URL", traceBaseURL); setErr != nil {
			fmt.Fprintf(os.Stderr, "warning: set ZOA_TRACE_CONTROL_URL: %v\n", setErr)
		} else {
			slog.Info("runtime trace control enabled", "base_url", traceBaseURL)
		}
		defer func() {
			if stopErr := stopRuntimeTraceControlServer(traceServer); stopErr != nil {
				fmt.Fprintf(os.Stderr, "warning: stop runtime trace control server: %v\n", stopErr)
			}
		}()
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

	appToken := keys.Resolve(appTokenFlag, "SLACK_APP_TOKEN")
	botToken := keys.Resolve(botTokenFlag, "SLACK_BOT_TOKEN")
	if appToken == "" {
		fmt.Fprintln(os.Stderr, "error: missing Slack app token (set --slack-app-token or SLACK_APP_TOKEN)")
		return 1
	}
	if botToken == "" {
		fmt.Fprintln(os.Stderr, "error: missing Slack bot token (set --slack-bot-token or SLACK_BOT_TOKEN)")
		return 1
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
		slog.Info("llmtrace server listening", "addr", llmtraceAddr)
	}

	client, err := gatewayclient.NewLocalGatewayClient(gatewayclient.LocalConfig{
		Session:       gatewayclient.DefaultSession,
		SessionDir:    sessionDir,
		CWD:           cwd,
		Model:         model,
		MaxTurns:      maxTurns,
		Temperature:   temperature,
		TimeoutSec:    timeoutSec,
		LLMTraceStore: traceStore,
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

	service, err := slackbridge.NewService(slackbridge.Config{
		AppToken:           appToken,
		BotToken:           botToken,
		OutboxPollInterval: time.Duration(pollMs) * time.Millisecond,
		OutboxLimit:        100,
	}, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing slack service: %v\n", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := service.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "slack runtime error: %v\n", err)
		return 1
	}
	return 0
}
