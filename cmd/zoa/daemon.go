package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	baselineagent "zoa/baselineagent"
	"zoa/internal/daemon"
	"zoa/internal/gatewayclient"
)

func runDaemon(args []string) int {
	if len(args) == 0 {
		printDaemonUsage()
		return 1
	}

	action := args[0]
	rest := args[1:]

	switch action {
	case "install":
		return daemonInstall(rest)
	case "uninstall":
		return daemonUninstall()
	case "start":
		return daemonLifecycle(daemon.ActionStart)
	case "stop":
		return daemonLifecycle(daemon.ActionStop)
	case "restart":
		return daemonLifecycle(daemon.ActionRestart)
	case "status":
		return daemonStatus()
	case "logs":
		return daemonLogs(rest)
	case "help", "-h", "--help":
		printDaemonUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown daemon action %q\n\n", action)
		printDaemonUsage()
		return 1
	}
}

func daemonInstall(args []string) int {
	fs := flag.NewFlagSet("daemon install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		cwd               string
		sessionDir        string
		model             string
		maxTurns          int
		temperature       float64
		timeoutSec        int
		pollMs            int
		logLevel          string
		debugLogComponent string
		traceHTTPAddr     string
	)

	defaultCWD := "/"
	defaultTimeout := 3600
	defaultPollMs := 400
	defaultLogLevel := "info"
	defaultTraceHTTPAddr := "127.0.0.1:3008"

	fs.StringVar(&logLevel, "log-level", defaultLogLevel, "Log level (debug, info, warn, error)")
	fs.StringVar(&debugLogComponent, "debug-log-component", "", "When set, only DEBUG logs with this component are emitted")
	fs.StringVar(&traceHTTPAddr, "trace-http-addr", defaultTraceHTTPAddr, "runtime trace control HTTP listen address for zoa slack (empty to disable)")
	fs.StringVar(&cwd, "cwd", defaultCWD, "Workspace root for tools and task context")
	fs.StringVar(&sessionDir, "session-dir", gatewayclient.DefaultSessionDir, "Directory for gateway sqlite persistence")
	fs.StringVar(&model, "model", baselineagent.DefaultModel, "Model identifier")
	fs.IntVar(&maxTurns, "max-turns", baselineagent.DefaultMaxTurns, "Max model turns per prompt")
	fs.Float64Var(&temperature, "temperature", baselineagent.DefaultTemperature, "Model temperature")
	fs.IntVar(&timeoutSec, "timeout", defaultTimeout, "Per-prompt timeout in seconds (0 disables timeout)")
	fs.IntVar(&pollMs, "poll-ms", defaultPollMs, "Outbox polling interval in milliseconds")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional args: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}

	cfg := daemon.InstallConfig{
		CWD:               cwd,
		SessionDir:        sessionDir,
		Model:             model,
		MaxTurns:          maxTurns,
		Temperature:       temperature,
		TimeoutSec:        timeoutSec,
		PollMs:            pollMs,
		LogLevel:          logLevel,
		DebugLogComponent: debugLogComponent,
		TraceHTTPAddr:     traceHTTPAddr,

		DefaultModel:             baselineagent.DefaultModel,
		DefaultMaxTurns:          baselineagent.DefaultMaxTurns,
		DefaultTemperature:       baselineagent.DefaultTemperature,
		DefaultTimeoutSec:        defaultTimeout,
		DefaultPollMs:            defaultPollMs,
		DefaultLogLevel:          defaultLogLevel,
		DefaultDebugLogComponent: "",
		DefaultCWD:               defaultCWD,
		DefaultSessionDir:        gatewayclient.DefaultSessionDir,
		DefaultTraceHTTPAddr:     defaultTraceHTTPAddr,
	}

	if err := daemon.Install(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func daemonUninstall() int {
	if err := daemon.Uninstall(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func daemonLifecycle(action daemon.Action) int {
	if err := daemon.RunAction(action); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func daemonStatus() int {
	info, err := daemon.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Print(info)
	return 0
}

func daemonLogs(args []string) int {
	if err := daemon.Logs(args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func printDaemonUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  zoa daemon install [flags]   Install and start the systemd user service")
	fmt.Fprintln(os.Stderr, "  zoa daemon uninstall         Stop and remove the service")
	fmt.Fprintln(os.Stderr, "  zoa daemon start             Start the service")
	fmt.Fprintln(os.Stderr, "  zoa daemon stop              Stop the service")
	fmt.Fprintln(os.Stderr, "  zoa daemon restart            Restart the service")
	fmt.Fprintln(os.Stderr, "  zoa daemon status            Show service status")
	fmt.Fprintln(os.Stderr, "  zoa daemon logs [-f] [-n N]  Show service logs")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Install flags (same as 'zoa slack'):")
	fmt.Fprintln(os.Stderr, "  --cwd, --model, --max-turns, --temperature,")
	fmt.Fprintln(os.Stderr, "  --timeout, --poll-ms, --session-dir, --log-level, --debug-log-component,")
	fmt.Fprintln(os.Stderr, "  --trace-http-addr")
}
