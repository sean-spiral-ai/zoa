package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"codexagentbase/internal/agent"
	"codexagentbase/internal/llm"
	"codexagentbase/internal/tools"
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
	flag.StringVar(&model, "model", "gemini-3-flash-preview", "Gemini model name")
	flag.IntVar(&maxTurns, "max-turns", 100, "Maximum model/tool loop iterations")
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

	toolset, err := tools.NewCodingTools(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: initialize tools: %v\n", err)
		os.Exit(1)
	}

	client := llm.NewGeminiClient(apiKey)
	session, err := agent.NewSession(agent.SessionConfig{
		Client:       client,
		Model:        model,
		Tools:        toolset,
		Temperature:  temperature,
		MaxTurns:     maxTurns,
		SystemPrompt: agent.DefaultSystemPrompt,
		VerboseLog:   verboseWriter(verbose),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: create session: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}

	result, err := session.Run(ctx, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent run failed after %d turns: %v\n", result.Turns, err)
		os.Exit(1)
	}

	fmt.Println(result.FinalResponse)
}

func verboseWriter(enabled bool) *os.File {
	if !enabled {
		return nil
	}
	return os.Stderr
}
