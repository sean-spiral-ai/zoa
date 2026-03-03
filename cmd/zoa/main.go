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
	case "inspect":
		return runInspect(args[1:])
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
	fmt.Fprintln(out, "  zoa inspect [flags] [sql]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Subcommands:")
	fmt.Fprintln(out, "  tui      Run the interactive gateway loop")
	fmt.Fprintln(out, "  slack    Connect the gateway loop to Slack Socket Mode DMs")
	fmt.Fprintln(out, "  inspect  Inspect SQLite stats or run an explicit SQL query")
}
