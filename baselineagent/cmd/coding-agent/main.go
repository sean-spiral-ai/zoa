package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	baselineagent "codexagentbase/baselineagent"
)

func main() {
	var (
		cwd         string
		prompt      string
		model       string
		maxTurns    int
		temperature float64
		timeoutSec  int
		verbose     bool
	)

	defaultCwd, _ := os.Getwd()

	flag.StringVar(&cwd, "cwd", defaultCwd, "Workspace root for tool execution")
	flag.StringVar(&prompt, "prompt", "", "User prompt for the coding agent")
	flag.StringVar(&model, "model", baselineagent.DefaultModel, "Gemini model name")
	flag.IntVar(&maxTurns, "max-turns", baselineagent.DefaultMaxTurns, "Maximum model/tool loop iterations")
	flag.Float64Var(&temperature, "temperature", 0.2, "Sampling temperature")
	flag.IntVar(&timeoutSec, "timeout", 600, "Run timeout in seconds")
	flag.BoolVar(&verbose, "verbose", false, "Print tool-call transcript to stderr")
	flag.Parse()

	if prompt == "" {
		prompt = strings.TrimSpace(strings.Join(flag.Args(), " "))
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "error: provide --prompt or trailing prompt args")
		os.Exit(2)
	}

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: GEMINI_API_KEY is not set")
		os.Exit(2)
	}

	result, err := baselineagent.Run(context.Background(), apiKey, baselineagent.RunConfig{
		CWD:         cwd,
		Instruction: prompt,
		Model:       model,
		MaxTurns:    maxTurns,
		Temperature: temperature,
		Timeout:     time.Duration(timeoutSec) * time.Second,
		VerboseLog:  verboseWriter(verbose),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent run failed after %d turns: %v\n", result.Turns, err)
		os.Exit(1)
	}

	fmt.Println(result.FinalResponse)
}

func verboseWriter(enabled bool) io.Writer {
	if !enabled {
		return nil
	}
	return os.Stderr
}
