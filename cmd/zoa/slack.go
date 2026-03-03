package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	baselineagent "zoa/baselineagent"
	"zoa/internal/gatewayclient"
	"zoa/internal/keys"
	slackbridge "zoa/internal/slack"
)

func runSlack(args []string) int {
	defaultCWD := "/"

	slackFlags := flag.NewFlagSet("slack", flag.ContinueOnError)
	slackFlags.SetOutput(os.Stderr)

	var (
		cwd         string
		sessionDir  string
		model       string
		maxTurns    int
		temperature float64
		timeoutSec  int
		pollMs      int

		appTokenFlag string
		botTokenFlag string
		logLevel     string
	)

	slackFlags.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	slackFlags.StringVar(&cwd, "cwd", defaultCWD, "Workspace root for tools and task context")
	slackFlags.StringVar(&sessionDir, "session-dir", gatewayclient.DefaultSessionDir, "Directory for gateway sqlite persistence")
	slackFlags.StringVar(&model, "model", baselineagent.DefaultModel, "Model identifier")
	slackFlags.IntVar(&maxTurns, "max-turns", baselineagent.DefaultMaxTurns, "Max model turns per prompt")
	slackFlags.Float64Var(&temperature, "temperature", baselineagent.DefaultTemperature, "Model temperature")
	slackFlags.IntVar(&timeoutSec, "timeout", 300, "Per-prompt timeout (seconds)")
	slackFlags.IntVar(&pollMs, "poll-ms", 400, "Outbox polling interval in milliseconds")

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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel,
	})))

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
