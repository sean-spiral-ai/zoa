package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 1
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	case "tui":
		return runTUI(args[1:])
	case "slack":
		return runSlack(args[1:])
	case "trace":
		return runTrace(args[1:])
	case "inspect":
		return runInspect(args[1:])
	case "daemon":
		return runDaemon(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "error: unknown subcommand %q\n\n", args[0])
		printUsage(os.Stderr)
		return 1
	}
}

func printUsage(out *os.File) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  zoa tui [flags]")
	fmt.Fprintln(out, "  zoa slack [flags]")
	fmt.Fprintln(out, "  zoa trace [flags]")
	fmt.Fprintln(out, "  zoa inspect [flags]")
	fmt.Fprintln(out, "  zoa inspect [flags] sql \"<sql>\"")
	fmt.Fprintln(out, "  zoa inspect [flags] conversation")
	fmt.Fprintln(out, "  zoa daemon <action> [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Subcommands:")
	fmt.Fprintln(out, "  tui      Run the interactive gateway loop")
	fmt.Fprintln(out, "  slack    Connect the gateway loop to Slack Socket Mode DMs")
	fmt.Fprintln(out, "  trace    Capture runtime trace, export Perfetto JSON, serve via trace_processor")
	fmt.Fprintln(out, "  inspect  Inspect SQLite stats, run SQL, or dump conversation history")
	fmt.Fprintln(out, "  daemon   Manage the systemd user service")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "LLM Trace:")
	fmt.Fprintln(out, "  Pass --llmtrace-addr :3009 to tui or slack to enable the LLM conversation tree viewer")
}
